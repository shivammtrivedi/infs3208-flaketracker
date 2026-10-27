#!/usr/bin/env bash
# Creates the GKE Standard cluster FlakeTracker runs on.
#
#   scripts/gcp/create-cluster.sh
#
# COST WARNING: three e2-medium nodes cost roughly USD 4-5 per day while running
# (about 125-150 per month if left on). Use scripts/gcp/scale-down.sh to stop paying
# for the nodes between sessions, and scripts/gcp/teardown.sh when completely done.
# Creating the cluster takes about 5-10 minutes.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

# SPOT=1 adds --spot: the nodes become Spot VMs (about a third of the price, but reclaimable at any time).
#   SPOT=1 scripts/gcp/create-cluster.sh
SPOT_FLAG=()
if [ "$SPOT" = 1 ]; then
  SPOT_FLAG=(--spot)
  KIND="Spot"
  COST="about USD 2 per day"
else
  KIND="on-demand"
  COST="about USD 4-5 per day"
fi

confirm "This will CREATE a GKE cluster named '$CLUSTER' (zonal, $NODES x e2-medium $KIND nodes, 30 GB disks).
It starts costing money immediately ($COST, plus the load balancer once deployed) until you scale it down or delete it."

# --zone                       a zonal cluster (one zone), which GKE doesn't charge a management fee for
# --num-nodes "$NODES"         three nodes by default: enough to spread replicas, drain one, and still have two
# --machine-type e2-medium     2 shared vCPUs, 4 GB RAM each: cheap, big enough for our small Go services
# --disk-type pd-balanced      the standard SSD-backed disk type
# --disk-size 30               GB boot disk per node
# --release-channel regular    Google keeps the Kubernetes version patched on the standard schedule
# --enable-ip-alias            pods get real VPC IP addresses ("VPC-native"); required for the
#                              container-native load balancing our Ingress uses
# --addons ...                 HttpLoadBalancing = the Ingress controller (creates the Google load balancer),
#                              HorizontalPodAutoscaling = lets the HPAs work (metrics-server is built in)
# --logging / --monitoring     send only SYSTEM data to Cloud Logging/Monitoring, not every application
#                              log line, to keep costs down (we read app logs with kubectl logs)
# --labels                     tags so the resources are recognisable in the console and billing
# --spot                       (only when SPOT=1) use Spot VMs
gcloud container clusters create "$CLUSTER" \
  --project "$PROJECT_ID" \
  --zone "$ZONE" \
  --num-nodes "$NODES" \
  --machine-type e2-medium \
  --disk-type pd-balanced \
  --disk-size 30 \
  --release-channel regular \
  --enable-ip-alias \
  --addons HttpLoadBalancing,HorizontalPodAutoscaling \
  --logging=SYSTEM \
  --monitoring=SYSTEM \
  --labels=app=flaketracker,course=infs3208 \
  "${SPOT_FLAG[@]}"

# kubectl needs a small helper program to log in to GKE with your Google account
# (Google's login code was removed from kubectl itself). Without it every kubectl
# command fails with "gke-gcloud-auth-plugin not found". Cloud Shell has it already;
# on your own machine this installs it once.
if ! command -v gke-gcloud-auth-plugin >/dev/null 2>&1; then
  gcloud components install gke-gcloud-auth-plugin --quiet
fi

# Point kubectl at the new cluster. Until this has run, kubectl falls back to
# localhost:8080 and every command fails.
gcloud container clusters get-credentials "$CLUSTER" --zone "$ZONE" --project "$PROJECT_ID"

echo
kubectl get nodes -o wide
echo
echo "Next: scripts/gcp/build-push.sh   (build and push the images)"
