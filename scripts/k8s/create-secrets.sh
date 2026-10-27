#!/usr/bin/env bash
# Creates the four Kubernetes Secrets the manifests refer to, on whichever
# cluster kubectl is currently pointed at (a Kind cluster or GKE). Passwords are
# generated here, at deploy time, and never written to a file or committed.
#
#   scripts/k8s/create-secrets.sh
#
# Optional: choose a value yourself by exporting it first, e.g.
#   INGEST_API_KEY=my-demo-key scripts/k8s/create-secrets.sh
#
# Safe to run again: a Secret that already exists is kept, not replaced. (A
# database's password is fixed when its disk is first initialised; silently
# changing the Secret afterwards would lock the services out of their database.)

# -e  stop at the first error      -u  treat unset variables as errors
# -o pipefail  a failure anywhere in a pipeline fails the whole pipeline
set -euo pipefail

NAMESPACE=flaketracker

# Print a random 32-character hex string (16 random bytes).
random() {
  # openssl is on most machines; otherwise read random bytes from the OS directly.
  openssl rand -hex 16 2>/dev/null || head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n'
}

# Create the namespace if it doesn't exist yet. "--dry-run=client -o yaml" prints
# the object instead of sending it; piping that into "kubectl apply" creates it
# only when missing, and does nothing if it's already there.
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# create_secret <secret-name> <key-inside-the-secret> <value>
create_secret() {
  local name="$1" key="$2" value="$3"
  if kubectl -n "$NAMESPACE" get secret "$name" >/dev/null 2>&1; then
    echo "Secret $name already exists: keeping it"
  else
    # --from-literal=KEY=VALUE   store this value under that key
    kubectl -n "$NAMESPACE" create secret generic "$name" --from-literal="$key=$value"
  fi
}

# One password per database: each service can only ever be given its own.
create_secret ingest-db-credentials   password "${INGEST_DB_PASSWORD:-$(random)}"
create_secret analyzer-db-credentials password "${ANALYZER_DB_PASSWORD:-$(random)}"
create_secret api-db-credentials      password "${API_DB_PASSWORD:-$(random)}"

# The key CI must send in the X-API-Key header when posting results to ingest.
create_secret ingest-api-key          api-key  "${INGEST_API_KEY:-$(random)}"

echo
echo "Done. To read the ingest API key (needed by the seed tool and the load test):"
echo "  kubectl -n $NAMESPACE get secret ingest-api-key -o jsonpath='{.data.api-key}' | base64 -d; echo"
