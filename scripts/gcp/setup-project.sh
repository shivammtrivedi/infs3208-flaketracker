#!/usr/bin/env bash
# One-time project setup, BEFORE creating the cluster:
#   1. enable the Google Cloud APIs we use
#   2. create the Artifact Registry repository our container images are pushed to
#   3. reserve the static IP address the Ingress will use
#   4. let Docker log in to the registry
#
#   scripts/gcp/setup-project.sh
#
# Cost: enabling APIs is free. The registry costs cents per GB per month (our images
# are ~0.2 GB in total). A reserved IP is free WHILE the Ingress uses it, and is
# billed by the hour if left unused, so scripts/gcp/teardown.sh releases it.
# Safe to run again: anything that already exists is left alone.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

confirm "This will enable the Kubernetes Engine / Artifact Registry / Compute APIs, create the
registry '$AR_REPO' and reserve the global static IP '$STATIC_IP_NAME'."

# 1. Turn on the APIs (a no-op if they're already enabled).
#   services enable  allow this project to use these Google services
gcloud services enable \
  container.googleapis.com \
  artifactregistry.googleapis.com \
  compute.googleapis.com \
  --project "$PROJECT_ID"

# 2. The image registry.
#   --repository-format=docker   it stores Docker images
#   --location                   the region the images are stored in (close to the cluster = fast pulls)
if gcloud artifacts repositories describe "$AR_REPO" --location "$REGION" --project "$PROJECT_ID" >/dev/null 2>&1; then
  echo "Registry $AR_REPO already exists"
else
  gcloud artifacts repositories create "$AR_REPO" \
    --repository-format=docker \
    --location "$REGION" \
    --description="FlakeTracker container images" \
    --project "$PROJECT_ID"
fi

# 3. A global static IP for the load balancer, so the public address never changes
#    even if the Ingress is deleted and re-created.
#   --global   the external HTTP(S) load balancer uses a global address
if gcloud compute addresses describe "$STATIC_IP_NAME" --global --project "$PROJECT_ID" >/dev/null 2>&1; then
  echo "Static IP $STATIC_IP_NAME already exists"
else
  gcloud compute addresses create "$STATIC_IP_NAME" --global --project "$PROJECT_ID"
fi
echo "The Ingress address will be: $(gcloud compute addresses describe "$STATIC_IP_NAME" --global --project "$PROJECT_ID" --format='value(address)')"

# 4. Teach Docker how to authenticate to Artifact Registry (adds an entry to your Docker config).
gcloud auth configure-docker "$REGION-docker.pkg.dev" --quiet

echo "Done. Next: scripts/gcp/create-cluster.sh"
