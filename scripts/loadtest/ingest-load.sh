#!/usr/bin/env bash
# Generates load against the ingest service so you can WATCH the autoscaler add pods
# (the scalability demo), and the drills (deleting pods, draining a node) can be done
# under traffic with an error count.
#
#   scripts/loadtest/ingest-load.sh <BASE_URL> <API_KEY> [WORKERS] [SECONDS]
#   e.g. scripts/loadtest/ingest-load.sh http://34.96.64.67 "$KEY" 24 120
#
# Get the API key with:
#   kubectl -n flaketracker get secret ingest-api-key -o jsonpath='{.data.api-key}' | base64 --decode
#
# What it sends: a realistic 150-test run, repeatedly, under the ID of a run that ALREADY
# exists (seed42-r1-1 from the demo data). ingest has to read, decode and validate the whole
# body (real CPU work), then recognises the duplicate and answers "200 duplicate" without
# writing anything, so the demo data is never changed. The demo data must be loaded first
# (scripts/k8s/seed.sh).
#
# Watch the autoscaler from another terminal while it runs:
#   kubectl -n flaketracker get hpa ingest -w
#   kubectl -n flaketracker get pods -l app=ingest -w
# Measured on GKE: 2 -> 5 replicas within 40 s, back to 2 about 2.5 minutes after the load stops.

set -euo pipefail

BASE_URL="${1:?usage: ingest-load.sh <BASE_URL> <API_KEY> [WORKERS] [SECONDS]}"
API_KEY="${2:?usage: ingest-load.sh <BASE_URL> <API_KEY> [WORKERS] [SECONDS]}"
WORKERS="${3:-24}"       # how many requests are in flight at once
DURATION="${4:-120}"     # how long to keep going, in seconds

WORK="$(mktemp -d)"      # scratch folder for the payload and the per-worker counters
trap 'rm -rf "$WORK"' EXIT

# ---- build the payload: 150 tests, one passing attempt each ----
{
  printf '{"repository":"acme/checkout-web","ci_run_id":"seed42-r1-1","ci_run_attempt":1,'
  printf '"branch":"main","commit_sha":"3f9c2a1e","started_at":"2026-08-20T10:00:00Z","results":['
  for i in $(seq 0 149); do
    [ "$i" -gt 0 ] && printf ','
    printf '{"suite":"tests/s%d.spec.ts","name":"test number %d does something","attempts":[{"status":"passed","duration_ms":%d}]}' \
      "$((i / 10))" "$i" "$((1000 + i))"
  done
  printf ']}'
} > "$WORK/payload.json"

# One request first, so a wrong URL or key fails immediately instead of after two minutes.
#   -s silent   -o /dev/null discard the body   -w print just the status code
first=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "$BASE_URL/ingest/v1/runs" \
  -H "X-API-Key: $API_KEY" --data-binary "@$WORK/payload.json")
if [ "$first" != 200 ]; then
  echo "A single test request returned HTTP $first (expected 200 'duplicate'): check the URL, the key, and that the demo data is loaded." >&2
  exit 1
fi

echo "Sending ~$(wc -c < "$WORK/payload.json") byte requests with $WORKERS workers for ${DURATION}s to $BASE_URL"
END=$(( $(date +%s) + DURATION ))

# ---- the workers: each loops, counting requests and any non-200 answers ----
#   -X POST            send a POST
#   -H 'X-API-Key: ..' the key ingest requires
#   --data-binary @f   send the file's contents as the request body
#   --max-time 10      give up on a request after 10 seconds (counts as an error)
for w in $(seq 1 "$WORKERS"); do
  (
    sent=0; bad=0
    while [ "$(date +%s)" -lt "$END" ]; do
      code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "$BASE_URL/ingest/v1/runs" \
        -H "X-API-Key: $API_KEY" --data-binary "@$WORK/payload.json" || true)
      sent=$((sent + 1))
      [ "$code" = 200 ] || bad=$((bad + 1))
    done
    echo "$sent $bad" > "$WORK/worker-$w.txt"
  ) &
done
wait    # until every worker has finished

# ---- summary ----
cat "$WORK"/worker-*.txt | awk -v d="$DURATION" '{n += $1; e += $2} END {
  printf "requests: %d   errors (non-200): %d (%.2f%%)   average rate: %.0f req/s\n", n, e, (n ? 100 * e / n : 0), n / d }'
