#!/usr/bin/env bash
# Brings the cluster back after scale-down.sh: three nodes again. Kubernetes then
# restarts every pod by itself and the databases reattach their disks, so the data
# is still there. Allow ~5 minutes for everything to become Ready.
#
#   scripts/gcp/scale-up.sh
# Starts costing roughly USD 4-5 per day again.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

confirm "This will resize the node pool of '$CLUSTER' to $NODES nodes (about USD 1.4 per node per day on-demand, about 0.5 on Spot)."

# --num-nodes "$NODES"   the VMs again (3 by default; a zonal cluster counts nodes in its single zone)
gcloud container clusters resize "$CLUSTER" \
  --node-pool "$NODE_POOL" \
  --num-nodes "$NODES" \
  --zone "$ZONE" \
  --project "$PROJECT_ID" \
  --quiet

# Make sure kubectl still points at this cluster.
gcloud container clusters get-credentials "$CLUSTER" --zone "$ZONE" --project "$PROJECT_ID"

echo "Waiting for the nodes..."
kubectl wait --for=condition=Ready nodes --all --timeout=600s

# scale-down.sh stopped every workload, so start them again: databases first (they reattach
# their disks, and the data is still there), then the services, which need the databases.
#   --replicas=1  one copy of each database      --replicas=2  two copies of each service
#   (the autoscalers take over ingest and api from here)
kubectl -n flaketracker scale statefulset --all --replicas=1
for db in ingest-db analyzer-db api-db; do
  kubectl -n flaketracker rollout status "statefulset/$db" --timeout=300s
done
kubectl -n flaketracker scale deployment --all --replicas=2
for d in ingest api analyzer web; do
  kubectl -n flaketracker rollout status "deployment/$d" --timeout=300s
done
kubectl -n flaketracker get pods -o wide
echo
echo "The load balancer is still there, so the site should answer again within a minute or two."
