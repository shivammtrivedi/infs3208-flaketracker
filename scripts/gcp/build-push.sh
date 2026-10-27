#!/usr/bin/env bash
# Builds the container images and pushes them to Artifact Registry, tagged with the
# git commit SHA. (This replaces the CI/CD pipeline that was cut: a release is
# "build-push.sh, then deploy.sh".)
#
#   scripts/gcp/build-push.sh                      all services, tag = current git commit
#   TAG=abc1234 scripts/gcp/build-push.sh          choose the tag yourself
#   SERVICES="analyzer" ALGORITHM=v2 TAG=abc1234-v2 scripts/gcp/build-push.sh
#                                                  just the analyzer, built with scoring algorithm v2
#                                                  (the image used for the rollout demo)
#
# Images are never tagged "latest": a specific tag per build is what makes
# `kubectl rollout history` and `kubectl rollout undo` meaningful.
# Prerequisites: scripts/gcp/setup-project.sh has been run, and Docker is running.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"   # repo root: Docker builds from here

# Short git SHA of the current commit, unless TAG was given.
TAG="${TAG:-$(git rev-parse --short HEAD)}"
SERVICES="${SERVICES:-ingest api analyzer migrate seed web}"
ALGORITHM="${ALGORITHM:-v1}"     # the analyzer's scoring algorithm, baked into the image

confirm "This will build [$SERVICES] with tag '$TAG' and push them to $REGISTRY."

for svc in $SERVICES; do
  image="$REGISTRY/$svc:$TAG"
  echo "== $svc -> $image"
  if [ "$svc" = web ]; then
    # The dashboard image: built from the web/ folder.
    #   -t  the image name and tag     (last argument: the build context folder)
    docker build --provenance=false --sbom=false -t "$image" web/
  else
    # All Go services share one Dockerfile; --build-arg SERVICE picks which program to build.
    #   VERSION / COMMIT / ALGORITHM  are stamped into the binary (shown in the dashboard footer)
    #   --provenance=false --sbom=false  skip the extra attestation data Docker Desktop adds by default
    docker build --provenance=false --sbom=false -f build/go.Dockerfile \
      --build-arg SERVICE="$svc" \
      --build-arg VERSION="$TAG" \
      --build-arg COMMIT="$TAG" \
      --build-arg ALGORITHM="$ALGORITHM" \
      -t "$image" .
  fi
  docker push "$image"
done

echo
echo "Pushed with tag: $TAG"
echo "Deploy it with:  scripts/k8s/deploy.sh gke $TAG"
