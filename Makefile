COLIMA_PROFILE := dfns-interview

.PHONY: help setup clean status logs test build deploy port-forward

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

setup: ## Run the complete setup (Colima + k3s + apps)
	@echo "Running setup..."
	./scripts/setup.sh

clean: ## Clean up everything (stop Colima, remove resources)
	@echo "Cleaning up..."
	./scripts/cleanup.sh

status: ## Show status of all resources
	@echo "=== Colima Status ==="
	colima status --profile $(COLIMA_PROFILE) || echo "Colima not running"
	@echo ""
	@echo "=== Kubernetes Nodes ==="
	kubectl get nodes
	@echo ""
	@echo "=== Helm Releases ==="
	helm list -n interview-test
	@echo ""
	@echo "=== Pods in interview-test namespace ==="
	kubectl get pods -n interview-test
	@echo ""
	@echo "=== Services ==="
	kubectl get svc -n interview-test

logs: ## Tail logs from all pods (use POD=name to specify)
ifdef POD
	kubectl logs -f $(POD) -n interview-test
else
	@echo "Usage: make logs POD=<pod-name>"
	@echo "Available pods:"
	@kubectl get pods -n interview-test -o name
endif

test: ## Run a quick smoke test
	@echo "Testing API service..."
	@kubectl run test-curl --rm -i --image=curlimages/curl --restart=Never -n interview-test -- \
		curl -s http://api-service:8080/orders && echo "API service responding" || echo "API service not responding"
	@echo ""
	@echo "Testing inventory service..."
	@kubectl run test-curl --rm -i --image=curlimages/curl --restart=Never -n interview-test -- \
		curl -s http://inventory-service:8081/products && echo "Inventory service responding" || echo "Inventory service not responding"

build: ## Build Docker images (k3s uses Docker runtime, so images are shared)
	@echo "Building images..."
	cd apps/api-service && docker build -t api-service:latest .
	cd apps/inventory-service && docker build -t inventory-service:latest .

deploy: ## Deploy/upgrade all Helm charts
	helm upgrade --install postgres ./charts/postgres -n interview-test
	helm upgrade --install vault hashicorp/vault -n interview-test --set "server.dev.enabled=true" --set "server.dev.devRootToken=root"
	helm upgrade --install api-service ./charts/api-service -n interview-test
	helm upgrade --install inventory-service ./charts/inventory-service -n interview-test

port-forward-api: ## Port forward to API service (localhost:8080)
	@echo "Port forwarding API service to localhost:8080"
	@echo "Press Ctrl+C to stop"
	kubectl port-forward -n interview-test svc/api-service 8080:8080

port-forward-inventory: ## Port forward to Inventory service (localhost:8081)
	@echo "Port forwarding Inventory service to localhost:8081"
	@echo "Press Ctrl+C to stop"
	kubectl port-forward -n interview-test svc/inventory-service 8081:8081

restart: ## Restart all deployments
	kubectl rollout restart deployment -n interview-test

describe: ## Describe all resources (useful for debugging)
	@echo "=== Deployments ==="
	kubectl describe deployments -n interview-test
	@echo ""
	@echo "=== Pods ==="
	kubectl describe pods -n interview-test
	@echo ""
	@echo "=== Services ==="
	kubectl describe services -n interview-test
