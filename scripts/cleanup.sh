#!/bin/bash
set -e

COLIMA_PROFILE="dfns-interview"

echo "Deleting Colima (profile: ${COLIMA_PROFILE})..."
colima delete --profile "${COLIMA_PROFILE}" --force 2>/dev/null || true

# Remove colima context from kubeconfig
kubectl config delete-context "colima-${COLIMA_PROFILE}" 2>/dev/null || true
kubectl config delete-cluster "colima-${COLIMA_PROFILE}" 2>/dev/null || true
kubectl config delete-user "colima-${COLIMA_PROFILE}" 2>/dev/null || true

echo "Done. To start fresh, run: ./scripts/setup.sh"
