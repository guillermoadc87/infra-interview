#!/bin/bash
set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

COLIMA_PROFILE="dfns-interview"
KUBE_CONTEXT="colima-${COLIMA_PROFILE}"

echo "=========================================="
echo "Platform Engineering Interview Setup"
echo "=========================================="
echo ""

# Check prerequisites
echo "Checking prerequisites..."
command -v colima >/dev/null 2>&1 || { echo -e "${RED}Error: colima is not installed. Install it first: brew install colima${NC}"; exit 1; }
command -v kubectl >/dev/null 2>&1 || { echo -e "${RED}Error: kubectl is not installed. Install it first: brew install kubectl${NC}"; exit 1; }
command -v tofu >/dev/null 2>&1 || { echo -e "${RED}Error: tofu is not installed. Install it first: brew install opentofu${NC}"; exit 1; }
command -v docker >/dev/null 2>&1 || { echo -e "${RED}Error: docker is not installed. Install it first: brew install docker${NC}"; exit 1; }
command -v helm >/dev/null 2>&1 || { echo -e "${RED}Error: helm is not installed. Install it first: brew install helm${NC}"; exit 1; }
echo -e "${GREEN}All prerequisites found${NC}"
echo ""

# Start Colima with k3s
echo "Starting Colima with k3s (profile: ${COLIMA_PROFILE})..."
if colima status --profile "${COLIMA_PROFILE}" >/dev/null 2>&1; then
    echo -e "${YELLOW}Colima (${COLIMA_PROFILE}) is already running${NC}"
else
    colima start \
        --profile "${COLIMA_PROFILE}" \
        --runtime docker \
        --kubernetes \
        --cpu 4 \
        --memory 8 \
        --disk 50
fi
echo -e "${GREEN}Colima is running${NC}"
echo ""

# Use the Colima kubectl context
kubectl config use-context "${KUBE_CONTEXT}"

# Wait for Kubernetes to be ready
echo "Waiting for Kubernetes to be ready..."
kubectl wait --for=condition=Ready nodes --all --timeout=300s
echo -e "${GREEN}Kubernetes is ready${NC}"
echo ""

# Create namespace
echo "Creating namespace..."
kubectl create namespace interview-test --dry-run=client -o yaml | kubectl apply -f -
echo -e "${GREEN}Namespace created${NC}"
echo ""

# Build Docker images (k3s uses Docker runtime, so images are shared)
echo "Building application images..."
docker build -t api-service:latest apps/api-service
docker build -t inventory-service:latest apps/inventory-service
echo -e "${GREEN}Images built${NC}"
echo ""

# Deploy PostgreSQL
echo "Deploying PostgreSQL..."
helm upgrade --install postgres ./charts/postgres \
    --namespace interview-test \
    --wait --timeout 300s
echo -e "${GREEN}PostgreSQL deployed${NC}"
echo ""

# Deploy Vault
echo "Deploying Vault..."
helm repo add hashicorp https://helm.releases.hashicorp.com 2>/dev/null || true
helm repo update
helm upgrade --install vault hashicorp/vault \
    --namespace interview-test \
    --set "server.dev.enabled=true" \
    --set "server.dev.devRootToken=root" \
    --wait --timeout 300s
echo -e "${GREEN}Vault deployed${NC}"
echo ""

# Deploy application services
echo "Deploying application services..."
helm upgrade --install api-service ./charts/api-service \
    --namespace interview-test \
    --wait --timeout 300s
helm upgrade --install inventory-service ./charts/inventory-service \
    --namespace interview-test \
    --wait --timeout 300s
echo -e "${GREEN}All services deployed${NC}"
echo ""

# Display status
echo "=========================================="
echo -e "${GREEN}Setup Complete!${NC}"
echo "=========================================="
echo ""
kubectl get pods -n interview-test
echo ""
echo "kubectl context is set to: ${KUBE_CONTEXT}"
echo ""
