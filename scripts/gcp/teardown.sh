#!/usr/bin/env bash
# Deletes EVERYTHING this project created in Google Cloud, so nothing keeps
# billing: the Ingress and its load balancer, the database disks, the cluster,
# the image registry and the static IP. Run it when the assessment is marked.
#
#   scripts/gcp/teardown.sh
#
# THIS DESTROYS THE DATABASES' DATA. It does not touch anything else in the project
# (your course VMs and their disks are left alone).
#
# The order matters. The load balancer and the disks are created by Kubernetes
# (from the Ingress and the PersistentVolumeClaims), and deleting the cluster alone
# can leave them behind, still costing money. So we delete those through Kubernetes first.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

confirm "This will DELETE the cluster '$CLUSTER' with all its data, the registry '$AR_REPO' and the static IP '$STATIC_IP_NAME'."

# Only try the Kubernetes clean-up if the cluster exists and has nodes to run it.
if gcloud container clusters describe "$CLUSTER" --zone "$ZONE" --project "$PROJECT_ID" >/dev/null 2>&1; then
  gcloud container clusters get-credentials "$CLUSTER" --zone "$ZONE" --project "$PROJECT_ID"

  # 1. Delete the Ingress: this makes GKE delete the Google load balancer it created.
  kubectl -n flaketracker delete ingress --all --ignore-not-found --timeout=120s || true
  # 2. Delete the databases and their volume claims: this deletes the persistent disks.
  kubectl -n flaketracker delete statefulset --all --ignore-not-found --timeout=120s || true
  kubectl -n flaketracker delete pvc --all --ignore-not-found --timeout=120s || true
  # Give GKE a minute to finish removing the load balancer and disks.
  sleep 60

  # 3. Delete the cluster itself (--quiet: no extra prompt; it takes a few minutes).
  gcloud container clusters delete "$CLUSTER" --zone "$ZONE" --project "$PROJECT_ID" --quiet
else
  echo "Cluster $CLUSTER not found: skipping"
fi

# 4. The image registry (and every image in it).
gcloud artifacts repositories delete "$AR_REPO" --location "$REGION" --project "$PROJECT_ID" --quiet || true

# 5. The static IP. An unused reserved IP is billed by the hour.
gcloud compute addresses delete "$STATIC_IP_NAME" --global --project "$PROJECT_ID" --quiet || true

echo
echo "Check nothing is left that costs money (each list should show nothing of ours):"
gcloud container clusters list --project "$PROJECT_ID"
gcloud compute forwarding-rules list --project "$PROJECT_ID"
gcloud compute addresses list --project "$PROJECT_ID"
gcloud compute disks list --project "$PROJECT_ID" --filter="name~^gke-OR name~^pvc-"
