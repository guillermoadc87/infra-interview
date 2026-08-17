#!/usr/bin/env bash
# Install Argo CD + Image Updater on the hub and plant the root Application.
#
# This is the one imperative step in the whole system, and that is inherent:
# something has to install the thing that installs everything else. After this
# runs, the ONLY supported way to change any cluster is a git commit.
#
# Required environment:
#   GH_OWNER      GitHub account owning the fork (also the GHCR namespace)
#   GH_PAT        token with Contents:read+write on the repo, for Image Updater's
#                 git write-back. The repo is public so Argo CD can READ it
#                 anonymously; this token exists only so the updater can WRITE.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

: "${GH_OWNER:?set GH_OWNER to the GitHub account that owns your fork}"
: "${GH_PAT:?set GH_PAT to a token with Contents read+write on the repo}"

REPO_URL="https://github.com/${GH_OWNER}/infra-interview"
HUB_CTX="$(ctx_for "$HUB_PROFILE")"

require_tools kubectl argocd

profile_running "$HUB_PROFILE" || die "hub cluster is not running -- run: ./scripts/cluster-up.sh hub"

# ---------------------------------------------------------------- Argo CD ----
info "installing Argo CD ${ARGOCD_VERSION} on the hub"
kubectl --context "$HUB_CTX" create namespace argocd --dry-run=client -o yaml \
  | kubectl --context "$HUB_CTX" apply -f - >/dev/null

# --server-side is REQUIRED: the Argo CD CRDs exceed the 262144-byte
# last-applied-configuration annotation limit that client-side apply uses.
kubectl --context "$HUB_CTX" -n argocd apply --server-side --force-conflicts \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml" >/dev/null

info "waiting for the Argo CD server"
kubectl --context "$HUB_CTX" -n argocd rollout status deploy/argocd-server --timeout=300s

# ------------------------------------------------------- repo credentials ----
# Argo CD reads this public repo anonymously, but Image Updater's
# write-back-method=git:repocreds reuses this same secret to PUSH. Hence a token.
info "registering repository write credentials"
kubectl --context "$HUB_CTX" -n argocd apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: repo-infra-interview
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  type: git
  url: ${REPO_URL}
  username: ${GH_OWNER}
  password: ${GH_PAT}
EOF

# --------------------------------------------------------- Image Updater ----
info "installing Argo CD Image Updater ${IMAGE_UPDATER_VERSION}"
kubectl --context "$HUB_CTX" -n argocd apply \
  -f "https://raw.githubusercontent.com/argoproj-labs/argocd-image-updater/${IMAGE_UPDATER_VERSION}/manifests/install.yaml" >/dev/null

# A single ImageUpdater CR covers every Application the ApplicationSets generate.
# useAnnotations:true tells it to read each Application's own annotations, which
# is the upstream-documented pattern for ApplicationSets -- otherwise you would
# need one CR per generated Application.
#
# The labelSelector is what keeps production out: the apps ApplicationSet only
# stamps `image-updater: enabled` onto non-prod Applications, so prod is
# unreachable by this controller even if its annotations were present.
info "applying the ImageUpdater CR"
kubectl --context "$HUB_CTX" -n argocd apply -f gitops/bootstrap/hub/imageupdater-cr.yaml >/dev/null

kubectl --context "$HUB_CTX" -n argocd rollout status deploy/argocd-image-updater --timeout=180s

# ------------------------------------------------------------- root app -----
info "planting the root Application (App-of-Apps)"
sed "s|__REPO_URL__|${REPO_URL}|g" gitops/root/root-application.yaml \
  | kubectl --context "$HUB_CTX" -n argocd apply -f - >/dev/null

ok "bootstrap complete"
echo
echo "  admin password:"
echo "    kubectl --context $HUB_CTX -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d"
echo "  UI / CLI:"
echo "    kubectl --context $HUB_CTX -n argocd port-forward svc/argocd-server 8090:443"
echo "    argocd login localhost:8090 --username admin --insecure"
echo
echo "  Now register a cluster and watch it provision itself:"
echo "    ./scripts/register-spoke.sh dev"
