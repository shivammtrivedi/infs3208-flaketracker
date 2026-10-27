#!/usr/bin/env bash
# Stops paying for the cluster's VMs without deleting it: resizes the node pool to
# zero nodes. Do this whenever you finish a work session.
#
#   scripts/gcp/scale-down.sh
#
# What stays (and what it costs):
#   * the cluster and its settings, the Kubernetes objects, the three database
#     disks (a few cents a day) and the load balancer + static IP (about USD 0.6 a day)
# What stops costing: the three VMs, which are the bulk of the bill.
# GKE does not charge a management fee for one zonal cluster. Bring it back with scale-up.sh.
# (To stop ALL charges, including the load balancer, use teardown.sh instead.)

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

confirm "This will stop the app and resize the node pool of '$CLUSTER' to 0 nodes. It stays offline until scale-up.sh."

# Point kubectl at this cluster (and fail early if it can't reach it).
gcloud container clusters get-credentials "$CLUSTER" --zone "$ZONE" --project "$PROJECT_ID"

# Stop the application FIRST, instead of letting GKE delete nodes under running pods.
# Deleting nodes makes GKE drain them, and the PodDisruptionBudgets (which keep at least one
# copy of each service alive) then block the drain: the pods have nowhere left to go, so the
# resize hangs for many minutes while the VMs keep billing. (Found the hard way.) With the
# workloads scaled to zero there is nothing to protect, and the databases also shut down
# cleanly rather than having their VM pulled out from under a running Postgres.
# Their disks and data are untouched; scale-up.sh starts everything again.
#   --all            every Deployment / StatefulSet in the namespace
#   --replicas=0     run no copies
kubectl -n flaketracker scale deployment --all --replicas=0
kubectl -n flaketracker scale statefulset --all --replicas=0
# Wait until every pod has actually gone.
kubectl -n flaketracker wait --for=delete pod --all --timeout=180s || true

# --node-pool        which pool to resize
# --num-nodes 0      zero VMs
# --quiet            don't ask gcloud's own confirmation (we asked above)
gcloud container clusters resize "$CLUSTER" \
  --node-pool "$NODE_POOL" \
  --num-nodes 0 \
  --zone "$ZONE" \
  --project "$PROJECT_ID" \
  --quiet
