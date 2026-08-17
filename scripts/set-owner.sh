#!/usr/bin/env bash
# Stamp your GitHub account into the manifests.
#
# The committed manifests carry the literal placeholder `OWNER` in two places
# that Argo CD reads directly from git and therefore cannot be substituted at
# apply time: ApplicationSet repoURLs, and the image references in each overlay's
# `images:` block.
#
# Run this once after forking, then commit the result.
#
# Usage: set-owner.sh <github-account>
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

NEW_OWNER="${1:-}"
[ -n "$NEW_OWNER" ] || die "usage: $0 <github-account>"

files=$(grep -rl --include='*.yaml' --include='*.json' -E 'github\.com/OWNER|ghcr\.io/OWNER' gitops .github 2>/dev/null || true)
[ -n "$files" ] || die "no placeholder occurrences found -- already stamped?"

echo "$files" | while read -r f; do
  # macOS and GNU sed disagree about -i; write via a temp file to work on both.
  sed -e "s|github\.com/OWNER|github.com/${NEW_OWNER}|g" \
      -e "s|ghcr\.io/OWNER|ghcr.io/${NEW_OWNER}|g" "$f" > "$f.tmp"
  mv "$f.tmp" "$f"
  echo "  updated $f"
done

ok "stamped owner '${NEW_OWNER}'. Remaining placeholders (should be none):"
grep -rn --include='*.yaml' --include='*.json' -E 'github\.com/OWNER|ghcr\.io/OWNER' gitops .github 2>/dev/null || echo "  none"
