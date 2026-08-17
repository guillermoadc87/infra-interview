#!/usr/bin/env bash
# The single reader and writer for CATEGORY 1 -- the container tag pinned in
# gitops/apps/<service>/envs/<env>/kustomization.yaml.
#
# WHY THIS EXISTS
#
# Argo CD Image Updater writes the deployed tag back into git
# (write-back-target: kustomization). That makes the overlay the authoritative
# record of what an environment is ACTUALLY running -- not a mirror of it, the
# record itself. So promotion never needs to be told which tag to promote: it
# can read it. Before this script, promote.yml asked a human to retype that
# value into a form field and checked only its shape, so a well-formed tag from
# an entirely different commit passed validation.
#
# Rollback works the same way one step further back: the git HISTORY of the same
# file is the list of tags an environment has actually run. That is a better
# source than the registry, which lists what was BUILT -- including images that
# never reached the environment at all.
#
# Every subcommand is read-only except `set`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib.sh
source "${ROOT}/scripts/lib.sh"

# The pipeline tag grammar: <env>-<UTC timestamp>-<7-char sha>. Fixed-width so
# lexical order equals chronological order, which is what Image Updater's
# `alphabetical` strategy relies on. Keep in sync with ci.yml and the
# allow-tags regex in gitops/appsets/apps-applicationset.yaml.
TAG_RE='^[a-z]+-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{7}$'

# An overlay that has never been promoted to carries this. It is a real,
# schema-valid tag so kustomize build and CI validation still pass -- it just
# does not exist in the registry.
PLACEHOLDER_RE='^[a-z]+-0{8}T0{6}Z-0{7}$'

usage() {
  cat >&2 <<'EOF'
usage: image-tag.sh <command> [args]

  current  <service> <env>          print the tag <env> currently runs
  previous <service> <env>          print the tag <env> ran before that
  history  <service> <env> [n]      print the last n distinct tags (default 10)
  set      <service> <env> <tag>    rewrite the pinned tag, one-line diff
  validate <tag> <env>              assert <tag> is a well-formed <env> tag

Paths are resolved relative to the repository root, so this works from anywhere.
EOF
  exit 64
}

overlay_for() {
  local svc="$1" env="$2" file="${ROOT}/gitops/apps/$1/envs/$2/kustomization.yaml"
  [ -f "$file" ] || die "no overlay for service '$svc' in env '$env' (looked for ${file#"${ROOT}"/})"
  echo "$file"
}

# Extract the newTag belonging to <service> from a kustomization on stdin.
#
# Anchored on the image `name:` line rather than just grepping for `newTag`,
# because a file may legitimately pin more than one image. Deliberately matches
# any registry/owner prefix so this keeps working after scripts/set-owner.sh
# rewrites the OWNER placeholder. Fails unless there is exactly one match --
# ambiguity here would mean silently promoting the wrong thing.
read_tag() {
  python3 -c '
import re, sys
svc = sys.argv[1]
src = sys.stdin.read()
pat = re.compile(r"-\s*name:\s*\S*/" + re.escape(svc) + r"\s*\n\s*newTag:\s*(\S+)")
found = pat.findall(src)
if len(found) != 1:
    sys.exit(f"expected exactly one newTag for {svc}, found {len(found)}")
print(found[0])
' "$1"
}

cmd_current() {
  [ $# -eq 2 ] || usage
  read_tag "$1" < "$(overlay_for "$1" "$2")"
}

# Every distinct tag this environment has run, newest first.
#
# Reads each historical revision of the overlay rather than parsing `git log -p`
# diffs: a revert re-introduces a tag the environment already ran, and the diff
# view makes that look like a new deployment. Consecutive duplicates are
# collapsed so a commit that changed something else in the file does not appear
# as a redeploy.
#
# Needs full history -- in CI, check out with fetch-depth: 0.
cmd_history() {
  [ $# -ge 2 ] || usage
  local svc="$1" env="$2" n="${3:-10}" file tags=""
  file="$(overlay_for "$svc" "$env")"

  local shas sha tag
  shas="$(git -C "$ROOT" log --format='%H' -- "$file")"
  [ -n "$shas" ] || die "no git history for ${file#"${ROOT}"/}"

  while read -r sha; do
    # A commit that predates the file, or a revision where the anchor does not
    # resolve, is not an error -- it just contributes no tag.
    tag="$(git -C "$ROOT" show "${sha}:${file#"${ROOT}"/}" 2>/dev/null | read_tag "$svc" 2>/dev/null || true)"
    # Drop the placeholder: it is what an overlay holds before its first
    # promotion, so it is never something the environment ran and never a
    # legitimate thing to roll back to.
    [ -n "$tag" ] && [[ ! "$tag" =~ $PLACEHOLDER_RE ]] && tags+="${tag}"$'\n'
  done <<< "$shas"

  [ -n "$tags" ] \
    || die "'$svc' has never been deployed to '$env' -- its overlay has only ever held the placeholder, so there is no history to draw on"
  # Collapse consecutive repeats, then take n. Assigned first rather than piped
  # straight into head, so head closing the pipe cannot SIGPIPE the loop above.
  printf '%s' "$tags" | awk '$0 != prev { print } { prev = $0 }' | head -n "$n"
}

cmd_previous() {
  [ $# -eq 2 ] || usage
  local prev
  prev="$(cmd_history "$1" "$2" 100 | sed -n '2p')"
  [ -n "$prev" ] \
    || die "'$1' has only ever run one tag on '$2' -- there is nothing to roll back to"
  echo "$prev"
}

# Surgical single-line edit -- deliberately NOT `kustomize edit set image`.
#
# `kustomize edit` round-trips the file through its YAML marshaller, which
# reformats every list and DELETES all comments. The first version of the
# promotion workflow used it and produced a prod PR whose diff was 20 lines of
# reindentation with the real change buried inside. A promotion PR exists to be
# reviewed by a human, so its diff must be one line.
cmd_set() {
  [ $# -eq 3 ] || usage
  local svc="$1" env="$2" tag="$3" file
  file="$(overlay_for "$svc" "$env")"

  python3 -c '
import re, sys
path, svc, tag = sys.argv[1], sys.argv[2], sys.argv[3]
src = open(path).read()
pat = re.compile(r"(-\s*name:\s*\S*/" + re.escape(svc) + r"\s*\n\s*newTag:\s*)(\S+)")
new, n = pat.subn(lambda m: m.group(1) + tag, src, count=1)
if n != 1:
    sys.exit(f"expected exactly one newTag for {svc} in {path}, found {n}")
open(path, "w").write(new)
' "$file" "$svc" "$tag"
  ok "${svc}/${env}: newTag -> ${tag}"
}

# Shape and ladder checks, in one place so promote.yml and rollback.yml cannot
# drift apart on what a valid tag is.
cmd_validate() {
  [ $# -eq 2 ] || usage
  local tag="$1" env="$2"
  [[ "$tag" =~ $TAG_RE ]] \
    || die "'$tag' does not match the pipeline tag format (<env>-<UTC timestamp>-<7-char sha>)"
  [[ "$tag" == "${env}-"* ]] \
    || die "'$tag' is not a ${env} tag"
  [[ ! "$tag" =~ $PLACEHOLDER_RE ]] \
    || die "'$tag' is the never-deployed placeholder -- nothing has ever been promoted to ${env}"
}

[ $# -ge 1 ] || usage
cmd="$1"; shift
case "$cmd" in
  current)  cmd_current  "$@" ;;
  previous) cmd_previous "$@" ;;
  history)  cmd_history  "$@" ;;
  set)      cmd_set      "$@" ;;
  validate) cmd_validate "$@" ;;
  *)        usage ;;
esac
