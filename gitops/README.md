# The GitOps repository

Three levels, matching the standard Argo CD pattern:

```
root/root-application.yaml     LEVEL 1  App-of-Apps -- the one thing applied by hand
  └── appsets/*.yaml           LEVEL 2  ApplicationSets -- discover what and where
        └── apps/ platform/    LEVEL 3  Kustomize manifests
```

## How a cluster provisions itself

`scripts/register-spoke.sh dev` writes one Secret on the hub carrying the label
`env=dev`. That label is the entire contract. Every ApplicationSet pairs a git
generator with a cluster generator:

```yaml
- git:
    directories: [{ path: gitops/apps/*/envs/* }]
- clusters:
    selector:
      matchLabels:
        env: "{{index .path.segments 4}}"     # <- read out of the repo path
```

`gitops/apps/api-service/envs/dev` has segments
`[gitops, apps, api-service, envs, dev]`, so **index 2 is the application and
index 4 is the environment**. Because the env label value is derived from the
path, one ApplicationSet spans every environment instead of one file per
environment. Register a cluster labelled `env=staging` and every application with
a `envs/staging` overlay appears on it, with no further instruction.

These indices are load-bearing, so `.github/workflows/gitops-validate.yml`
re-checks the invariants on every change.

## Adding things

- **A new environment**: create `envs/<name>/` under each app, register a cluster
  with `--label env=<name>`. Nothing else.
- **A new application**: create `apps/<name>/base` and `apps/<name>/envs/<env>/`.
  Auto-discovered.
- **A new platform component**: add `platform/<name>/envs/<env>/kustomize.json`
  (Kustomize) or `config.json` (upstream Helm chart). The *filename* is what
  routes it to the right ApplicationSet.

## Where configuration lives, and why

Kubernetes environment configuration falls into four categories that behave
differently under promotion. Conflating them is what makes environments drift, so
each category has one physical home and one mechanism.

| # | Category | File | Mechanism | Promoted? |
|---|---|---|---|---|
| 1 | Application version (container tag) | `apps/<app>/envs/<env>/kustomization.yaml` | `images[].newTag` | **Yes** |
| 1' | Platform chart version | `platform/<c>/envs/<env>/config.json` | `chartVersion` | **Yes** |
| 2 | Kubernetes settings — replicas | `apps/<app>/envs/<env>/replicas.yaml` | strategic-merge patch | No |
| 2 | Kubernetes settings — resources | `apps/<app>/envs/prod/resources.yaml` | strategic-merge patch | No |
| 2 | Kubernetes settings — probes, ports, securityContext | `apps/<app>/base/deployment.yaml` | base | n/a |
| 2 | Kubernetes settings — prod-only resources | `apps/<app>/envs/prod/pdb.yaml` | `resources:` | No |
| 3 | Mostly-static business settings, tier-scoped | `apps/<app>/variants/tier/{non-prod,prod}/` | Component | **Never** |
| 3 | Environment identity | `apps/<app>/variants/env/<env>/` | Component | **Never** |
| 4 | Non-static business settings | `apps/<app>/envs/<env>/settings.yaml` | strategic-merge patch | **Yes** |
| — | Environment-invariant config (`DB_HOST`, `PORT`) | `apps/<app>/base/deployment.yaml` | base | n/a |
| — | Secrets | **not in git** — `bootstrap/spoke/` | out of band | n/a |

The "never promoted" claim is structural, not a convention: promotion only ever
touches `envs/<env>/settings.yaml` and the `images:` block. Nothing under
`variants/` is reachable from a promotion, and no file under `envs/` names
`PAYMENTS_URL`. **You cannot accidentally promote the sandbox payment endpoint
into production**, because there is no file to copy that would do it.

### Two variant axes, not one

`variants/tier/` answers "is this real customer traffic" (payment endpoints, log
levels). `variants/env/` answers "which environment is this" (`ENVIRONMENT`).
They are separate because a two-valued tier cannot express a three-valued fact,
and folding environment identity into `envs/` would place a never-promoted value
inside the directory promotion copies *from*.

### Why the image tag is not a `version.yaml` patch

The well-known reference model for this taxonomy
([kostis-codefresh/gitops-environment-promotion](https://github.com/kostis-codefresh/gitops-environment-promotion))
keeps the container tag in a `version.yml` strategic-merge patch, which makes
promotion a clean single-file copy. This repository deliberately uses the
`images:` transformer instead.

The reason is that Argo CD Image Updater's `write-back-target: kustomization`
writes `images[].newTag`. If a patch file *also* set `.image`, Kustomize's
ImageTagTransformer runs **after** patches, so `images:` would silently win — and
`version.yaml` would become a file that reads as authoritative, gets reviewed as
authoritative, and does nothing. That is worse than an error, because it fails
silently. So the tag has exactly one owner, and CI fails the build if any file
under `envs/` or `variants/` names an image.

The cost is that category 1 no longer promotes by `cp`. It is paid by making
promotion a workflow that uses the same field the robot uses.

## Promotion

```
dev --------> staging --------> prod
     retag         retag + PR
```

`.github/workflows/promote.yml` retags an existing digest; it never rebuilds, so
the bytes that passed staging are the bytes production runs. It refuses to
promote an image without a `linux/arm64` manifest, and fails loudly if the digest
changes during the retag.

- **staging** is watched by Image Updater (`allow-tags: ^staging-.*`), so the
  retag alone is enough.
- **prod is watched by nothing, deliberately.** The workflow opens a pull request
  moving the overlay tag and carrying `settings.yaml` with it. Production sync is
  also manual, so a merge does not by itself deploy.

### You do not tell it which tag to promote

Pick a service and a target, and run it. `source_tag` is optional.

The point of Image Updater's git write-back is that `envs/<env>/kustomization.yaml`
*is* the record of what an environment runs — not a copy of it kept roughly in
sync. So promotion reads it (`scripts/image-tag.sh current`) instead of asking a
human to find that string in GHCR and paste it back in. The same helper writes
the target overlay, so exactly one piece of code understands where the tag lives.

This is stricter than a text field, not looser. What gets promoted is by
construction the artifact the source environment actually converged on; the old
form accepted any well-formed tag, including one from an unrelated commit.

Resolution is **per service**, which is the only correct behaviour: the services
build independently, so a merge touching one does not move the other and their
tags legitimately differ. Filling in `source_tag` therefore restricts you to a
single service — one tag names one artifact, and it cannot mean two things across
a fan-out.

Use `source_tag` only to deliberately promote something *older*. The run warns
when it differs from what the source environment is running, so an override is
never quiet.

One caveat, because it bites in practice: a staging promotion only retags the
registry, and Image Updater moves the staging overlay afterwards on its own
schedule. Promote to prod in that window and you resolve the *previous* staging
tag. The workflow compares the overlay against GHCR and warns when it sees a
newer `staging-` tag, i.e. when the write-back has not landed yet.

## Rollback, and the trap in it

**`git revert` does not roll back dev or staging.** The pipeline's rule is "the
newest allowed tag in the registry wins", so reverting the commit that bumped
`newTag` just means Image Updater re-reads the registry, still finds the bad tag
as newest, and writes it back. The revert is undone automatically.

The correct rollback is to roll the registry **forward**: re-promote a known-good
digest under a new, higher-sorting tag. Same mechanism as promotion, one command,
and it leaves a forward-only audit trail.

```bash
GOOD_DIGEST=$(docker buildx imagetools inspect ghcr.io/<owner>/api-service:<good-tag> \
                --format '{{ .Manifest.Digest }}')
docker buildx imagetools create \
  --tag ghcr.io/<owner>/api-service:dev-$(date -u +%Y%m%dT%H%M%SZ)-<sha> \
  ghcr.io/<owner>/api-service@$GOOD_DIGEST
```

To *stop* deployments rather than change them, scale Image Updater to zero, then
revert. On **prod none of this applies** — nothing is racing you, so `git revert`
plus a manual sync is a true rollback. That asymmetry is a large part of why prod
is gated the way it is.

`.github/workflows/rollback.yml` does the above for you, and like promotion it
does not need to be told the tag: leave `good_tag` blank and it returns to the
tag that environment ran *before* the current one.

That list comes from the git history of `envs/<env>/kustomization.yaml`
(`scripts/image-tag.sh previous`), which is a better source than the registry —
git records what was **deployed**, GHCR records what was **built**, and those
differ every time an image is superseded before it rolls out. The history is read
by replaying each revision of the file rather than by diffing, so a revert shows
up correctly as the environment running that tag a second time.

`workflow_dispatch` cannot populate a dropdown from data, so if one step back is
not far enough, the run prints the real candidate tags in its summary — copy one
from there rather than going hunting in GHCR.

## Ordering

Sync waves order resources *within* one Application; they do **not** order
Applications generated by different ApplicationSets, and an ApplicationSet
reports Healthy once it has *generated* its Applications, not once they are
healthy. So ordering across applications is done two ways that actually work:

1. A `wait-for-postgres` **PreSync hook Job** in each app's `base/` holds the
   sync open until the database accepts connections, then fails cleanly on
   timeout and retries under the Application's backoff.
2. The applications themselves retry their database connection in the background
   and report `/readyz` accordingly, so ordering stops being load-bearing at all.

Sync waves are still used where they do apply: `-100` for AppProjects, `-20`/`-10`
for platform, `0` for apps, all inside the root app's single sync.
