#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLUSTER_NAME="kattle-test"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

# Check prerequisites
for cmd in kind kubectl; do
  if ! command -v "$cmd" &>/dev/null; then
    error "$cmd is required but not found. Install it first."
    exit 1
  fi
done

# 1. Create cluster (idempotent)
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
  info "Cluster '${CLUSTER_NAME}' already exists, skipping creation."
else
  info "Creating Kind cluster '${CLUSTER_NAME}'..."
  kind create cluster --name "$CLUSTER_NAME" --config "${SCRIPT_DIR}/kind.yaml"
  info "Cluster created."
fi

# 2. Ensure namespace + default ServiceAccount exist before creating bare Pods
CTX="kind-${CLUSTER_NAME}"
info "Ensuring namespace kattle-test..."
kubectl --context "$CTX" create namespace kattle-test 2>/dev/null || true
info "Waiting for default ServiceAccount..."
kubectl --context "$CTX" -n kattle-test get serviceaccount default &>/dev/null || \
  kubectl --context "$CTX" -n kattle-test wait --for=jsonpath='{.metadata.name}'=default \
    serviceaccount/default --timeout=30s 2>/dev/null || \
  sleep 5

# 3. Apply manifests (declarative, idempotent)
info "Applying workloads..."
kubectl --context "$CTX" apply -f "${SCRIPT_DIR}/manifests/workloads.yaml"

# 4. Wait for pods to be ready
info "Waiting for deployments to be ready..."
kubectl --context "$CTX" -n kattle-test \
  rollout status deployment/web --timeout=120s
kubectl --context "$CTX" -n kattle-test \
  rollout status deployment/api --timeout=120s

info "Waiting for standalone pods..."
kubectl --context "$CTX" -n kattle-test \
  wait --for=condition=ready pod -l app=standalone --timeout=120s
kubectl --context "$CTX" -n kattle-test \
  wait --for=condition=ready pod/multi-container --timeout=120s
kubectl --context "$CTX" -n kattle-test \
  wait --for=condition=ready pod/rich-metadata --timeout=120s

# 5. Status summary
echo ""
info "=== Cluster Ready ==="
echo ""
kubectl --context "$CTX" -n kattle-test get all
echo ""
info "Context: kind-${CLUSTER_NAME}"
info "Namespace: kattle-test"
echo ""
info "Next steps:"
info "  KATTLE_USE_SQLSTORE=1 wails dev"
info "  Select context 'kind-${CLUSTER_NAME}' -> Pods"
echo ""
info "Cleanup:"
info "  kind delete cluster --name ${CLUSTER_NAME}"
