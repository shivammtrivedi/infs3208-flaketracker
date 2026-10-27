#!/usr/bin/env bash
# Shared settings for the scripts in this folder. Each script loads them with:
#   source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
# Every value can be overridden by exporting the variable first, e.g.
#   ZONE=australia-southeast2-a scripts/gcp/create-cluster.sh

# The GCP project: whatever gcloud is currently set to. The scripts print it before
# doing anything so you can see which project (and which Google account) they will use.
# Check it yourself with:   gcloud config list
PROJECT_ID="${PROJECT_ID:-$(gcloud config get-value project 2>/dev/null)}"
ACCOUNT="$(gcloud config get-value account 2>/dev/null)"

# Where everything lives. Melbourne, and the same zone used in earlier coursework.
# (If a zone is overloaded and the cluster won't create, retry with ZONE=australia-southeast2-a or -c.)
REGION="${REGION:-australia-southeast2}"
ZONE="${ZONE:-australia-southeast2-b}"

# Names. The cluster and registry names must be typed identically everywhere
# they're used; typos can't be renamed afterwards, only recreated.
CLUSTER="${CLUSTER:-flaketracker}"
AR_REPO="${AR_REPO:-flaketracker}"            # the Artifact Registry repository holding our images
STATIC_IP_NAME="${STATIC_IP_NAME:-flaketracker-ip}"   # must match the annotation in deploy/k8s/overlays/gke/ingress.yaml
NODE_POOL="${NODE_POOL:-default-pool}"

# How many nodes, and whether to use cheaper Spot VMs (SPOT=1). Spot VMs cost roughly a
# third as much, but Google can reclaim them at any moment, so a pod may restart without
# warning. Fine for building and testing; do NOT use them for the marked demo.
NODES="${NODES:-3}"
SPOT="${SPOT:-0}"

# Where images are pushed to and pulled from.
REGISTRY="$REGION-docker.pkg.dev/$PROJECT_ID/$AR_REPO"

# Show what we're about to act on and ask for a yes before spending credit.
#   confirm "what the script is about to do"
# Set ASSUME_YES=1 to skip the question (for re-runs you've already approved).
confirm() {
  echo
  echo "Account : $ACCOUNT"
  echo "Project : $PROJECT_ID"
  echo "Region  : $REGION   Zone: $ZONE"
  echo
  echo "$1"
  echo
  if [ "${ASSUME_YES:-0}" = 1 ]; then return 0; fi
  read -r -p "Continue? [y/N] " answer
  [ "$answer" = y ] || [ "$answer" = Y ] || { echo "Cancelled; nothing was changed."; exit 1; }
}
