#!/usr/bin/env bash
# Loads the demo data (135,000 test results) into the cluster's ingest database.
#
#   scripts/k8s/seed.sh local            # Kind cluster
#   scripts/k8s/seed.sh gke <TAG>        # GKE, using the seed image tagged <TAG>
#
# It runs the seed tool as a Kubernetes Job INSIDE the cluster, so the database is
# never exposed to the internet. The analyzer then picks the data up through
# ingest's feed (about 15-30 seconds) and the dashboard fills in.

set -euo pipefail

ENVIRONMENT="${1:?usage: seed.sh local | seed.sh gke <TAG>}"
NAMESPACE=flaketracker
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

case "$ENVIRONMENT" in
  local) IMAGE="flaketracker/seed:local" ;;
  gke)
    TAG="${2:?usage: seed.sh gke <TAG>}"
    PROJECT_ID="$(gcloud config get-value project 2>/dev/null)"
    IMAGE="${REGION:-australia-southeast2}-docker.pkg.dev/$PROJECT_ID/${AR_REPO:-flaketracker}/seed:$TAG"
    ;;
  *) echo "unknown environment '$ENVIRONMENT' (use: local or gke)" >&2; exit 2 ;;
esac

# A Job can't be edited once created, so remove any previous run first.
kubectl -n "$NAMESPACE" delete job seed --ignore-not-found --wait=true

# sed replaces the SEED_IMAGE placeholder in the manifest; the result is piped to kubectl.
#   -f -   read the manifest from standard input
sed "s#SEED_IMAGE#$IMAGE#" "$REPO_ROOT/deploy/k8s/tools/seed-job.yaml" | kubectl apply -f -

# Wait for the load to finish (it takes a few seconds), then show what it printed.
kubectl -n "$NAMESPACE" wait --for=condition=complete job/seed --timeout=300s
kubectl -n "$NAMESPACE" logs job/seed
