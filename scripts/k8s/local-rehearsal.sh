#!/usr/bin/env bash
# Rehearses the whole Kubernetes deployment on your own machine, for free,
# BEFORE touching GCP. It creates a local Kind cluster (one control-plane node
# and three workers, each a Docker container), builds the images, loads them into
# the cluster, and deploys with the same script and manifests as the real thing.
#
#   scripts/k8s/local-rehearsal.sh          create the cluster and deploy
#   kind delete cluster --name flaketracker  throw it all away afterwards
#
# Prerequisites: Docker running, plus kubectl and kind installed.
# What it cannot test: the Google Ingress and load balancer (those exist only on GKE).

set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"   # repo root

CLUSTER=flaketracker
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo local)"

# ---- 1. The cluster ----
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  echo "== Kind cluster '$CLUSTER' already exists: reusing it"
else
  echo "== Creating the Kind cluster (the first run downloads the node image, a few minutes)"
  kind create cluster --config deploy/k8s/kind-config.yaml
fi
# Point kubectl at it (Kind names the context kind-<cluster>).
kubectl config use-context "kind-$CLUSTER"

# ---- 2. The images ----
# One Dockerfile builds every Go service; --build-arg SERVICE picks which.
#   --provenance=false --sbom=false   skip the extra attestation data Docker Desktop
#                                     adds by default, which `kind load` cannot import
echo "== Building the images"
for svc in ingest api analyzer migrate seed; do
  docker build --provenance=false --sbom=false -f build/go.Dockerfile \
    --build-arg SERVICE="$svc" --build-arg COMMIT="$COMMIT" -t "flaketracker/$svc:local" .
done
docker build --provenance=false --sbom=false -t flaketracker/web:local web/

# ---- 3. Copy the images into the Kind nodes (Kind can't see your local Docker images otherwise) ----
echo "== Loading the images into the cluster"
kind load docker-image --name "$CLUSTER" \
  flaketracker/ingest:local flaketracker/api:local flaketracker/analyzer:local \
  flaketracker/migrate:local flaketracker/seed:local flaketracker/web:local

# ---- 4. metrics-server: supplies the CPU numbers the autoscalers read ----
# GKE has it built in; Kind does not. Kind's kubelets use self-signed certificates,
# so metrics-server needs --kubelet-insecure-tls (acceptable for a throw-away local cluster only).
echo "== Installing metrics-server"
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
kubectl -n kube-system patch deployment metrics-server --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'

# ---- 5. Secrets, then the application ----
echo "== Creating Secrets"
scripts/k8s/create-secrets.sh
echo "== Deploying"
scripts/k8s/deploy.sh local
