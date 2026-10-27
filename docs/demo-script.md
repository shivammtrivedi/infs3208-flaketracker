# FlakeTracker demo script

**Format:** 6 minutes of demo + 2 minutes of questions, on the deployed GKE cluster.
**Marks are in the deployment half**, so the tech-stack talk is kept to one minute and the
deployment gets four.

Values used below (all measured on GKE on 2026-10-20; re-time them in your rehearsal):

| Thing | Value |
|---|---|
| Public address | `http://34.96.64.67` |
| Image tag now deployed | `oct20-1` (analyzer v2 image: `oct20-1-v2`, already in the registry) |
| Demo data | 74 flaky, 15 broken, 361 stable (algorithm v1); 50 flaky under v2 |
| Autoscaling seen | `ingest` 2 -> 5 pods in under 40 s, back down about 2.5 min after the load stops |
| Rollout seen | analyzer v2 rolled out in about 7 s, visible on the dashboard after about 22 s |

---

## 1. Before the demo (start 25 minutes early)

```bash
# 1. Start the cluster: 3 VMs, then the databases, then the services (about 5 minutes).
#    It asks y/N first; ASSUME_YES=1 answers yes for you.
ASSUME_YES=1 scripts/gcp/scale-up.sh

# 2. Every pod should be Running and READY (databases 1/1, services 2/2 copies).
#    -n flaketracker = our namespace;  -o wide = also show which node each pod is on.
kubectl -n flaketracker get pods -o wide

# 3. The public address must answer 200. If you see 502, wait 1-2 minutes and retry.
#    -s silent;  -o /dev/null throw the page away;  -w '%{http_code}' print only the status code.
curl -s -o /dev/null -w '%{http_code}\n' http://34.96.64.67/healthz

# 4. The data survived: the summary should show 450 tests (74 flaky, 15 broken, 361 stable).
curl -s http://34.96.64.67/api/v1/summary

# 5. Put the ingest API key in a variable (it is never printed on screen).
KEY=$(kubectl -n flaketracker get secret ingest-api-key -o jsonpath='{.data.api-key}' | base64 -d)

# 6. The registry path used by the rollout commands.
REGISTRY=australia-southeast2-docker.pkg.dev/$(gcloud config get-value project)/flaketracker
```

Open three terminal windows and one browser, with these ready but **not yet run**:

| Window | Holds |
|---|---|
| Browser | `http://34.96.64.67` on the Overview page |
| Terminal A (main) | the `kubectl` commands in section 2 |
| Terminal B | `kubectl -n flaketracker get hpa -w` (the autoscaler watch) |
| Terminal C | the load test command (start it at 2:00) |

Check the dashboard shows **v1** in the footer, and no test is quarantined.

---

## 2. The 6 minutes

### 0:00 - 1:00  The app (browser)

*Say:* "FlakeTracker finds flaky tests: tests that pass and fail on the same code. It reads CI results, scores every test, and ranks them."

1. **Overview** (15 s): totals (tests, flaky, broken, quarantined, runs) and the trend chart.
2. **Leaderboard** (15 s): ranked by score. Filter to *flaky*, search a name.
   One-liner if asked why a test with a higher failure rate can be "stable" or "broken": *a test that always fails is broken, not flaky; flaky means it passes on retry.*
3. **Test detail** (15 s): open the top test. Show the score breakdown, the pass/fail timeline and the duration chart.
4. **Quarantine** (10 s): quarantine that test with a reason, show it appear, then release it so the counts go back.
5. **Scoring** (5 s): point at the footer (algorithm v1, commit). "The scoring algorithm is part of the image; we change it live later."

### 1:00 - 2:00  Tech stack (show the architecture diagram)

*Say:* "A React and TypeScript front end served by nginx. Three Go microservices, `ingest`, `analyzer` and `api`, each with its own PostgreSQL database, talking only over internal HTTP. Everything is a small Docker image, tagged with its Git commit, stored in Artifact Registry, and run on Google Kubernetes Engine."

Point at the diagram: browser -> load balancer -> `web` / `api` / `ingest`; `api` calls `analyzer` and `ingest`; `analyzer` pulls `ingest`'s results feed; each service touches only its own database.

### 2:00 - 2:40  Deployment choices (Terminal A)

**First, start the load in Terminal C** (it needs about 40 s to trigger scaling, so start it before you talk):

```bash
# 24 parallel workers for 120 seconds. The requests replay an existing run, so ingest does real
# work (decode + validate) but no data changes. The last line prints the request and error count.
scripts/loadtest/ingest-load.sh http://34.96.64.67 "$KEY" 24 120
```

Start Terminal B's watch as well (`kubectl -n flaketracker get hpa -w`), then in Terminal A:

```bash
# One cluster, three nodes.
kubectl get nodes

# Every pod and the node it is on: replicas are spread across nodes.
kubectl -n flaketracker get pods -o wide

# The four services (Deployments) and the three databases (StatefulSets) are separate objects.
kubectl -n flaketracker get deploy,sts
```

*Say:* "One zonal GKE cluster with three e2-medium nodes. Four services run independently, and each data service has its own database pod, so they scale, fail and update on their own."

### 2:40 - 3:10  Orchestration and load balancing

```bash
# The whole system as objects Kubernetes manages from YAML files:
# services, databases, migration Jobs, Services, Ingress, autoscalers and disruption budgets.
kubectl -n flaketracker get deploy,sts,svc,ingress,hpa,pdb,jobs

# The Ingress is the Google load balancer; its ADDRESS is the public IP.
kubectl -n flaketracker get ingress flaketracker
```

*Say:* "Kubernetes runs the whole system from YAML. The Ingress creates a Google HTTP load balancer that routes `/`, `/api` and `/ingest` to the right service, and each Service then spreads requests across that service's pods." (Optionally show `deploy/k8s/base/30-ingest.yaml` in the editor for five seconds.)

### 3:10 - 4:00  Scalability and reliability (Terminals B and A)

1. **Scalability:** show Terminal B. The `ingest` autoscaler's TARGETS climb past 60% and REPLICAS rises (2 -> 5 or more).
   *Say:* "A Horizontal Pod Autoscaler adds pods when CPU passes 60%, and removes them afterwards."
2. **Reliability:** while the load is running, delete a pod:

```bash
# Delete one ingest pod under traffic. Kubernetes replaces it, and the other replicas keep serving.
# --wait=false returns immediately instead of waiting for the pod to go away.
kubectl -n flaketracker delete pod -l app=ingest --wait=false
```

   *Say:* "Every service runs at least two replicas across nodes, with health checks and disruption budgets, so losing a pod or a node doesn't take the site down. If one database is lost, only the pages that need it show an error."
3. Refresh the dashboard to show it still works. At about 4:00 the load finishes and Terminal C prints the totals; read the error count out ("about zero").

### 4:00 - 5:30  Rollout and rollback (Terminal A, then browser)

```bash
# --- Rollout: switch the analyzer to the v2 scoring-algorithm image ---
# Record WHY first (annotate BEFORE set image so the note lands on the new revision).
kubectl -n flaketracker annotate deployment/analyzer kubernetes.io/change-cause="v2: recency-weighted scoring" --overwrite
# Point the Deployment at the v2 image. Pods are replaced one at a time, never fewer than the current number.
kubectl -n flaketracker set image deployment/analyzer analyzer=$REGISTRY/analyzer:oct20-1-v2
# Wait until the rollout finishes (about 7 seconds).
kubectl -n flaketracker rollout status deployment/analyzer
```

Refresh the browser after about 20 seconds: the footer now says **algorithm v2** and the flaky count drops from 74 to 50.
*Say:* "A different algorithm is a different image, rolled out with zero downtime."

```bash
# --- Rollback: a deliberately broken release ---
# Note the reason, then point the api at an image that does not exist.
kubectl -n flaketracker annotate deployment/api kubernetes.io/change-cause="bad release: missing image" --overwrite
kubectl -n flaketracker set image deployment/api api=$REGISTRY/api:does-not-exist

# The new pod sits in ImagePullBackOff while the old pods keep serving (refresh the dashboard to prove it).
kubectl -n flaketracker get pods -l app=api

# One command puts the previous working version back.
kubectl -n flaketracker rollout undo deployment/api

# The history of revisions, each with the reason it was made.
kubectl -n flaketracker rollout history deployment/api
```

*Say:* "Images are tagged with their Git commit. A bad release stalls instead of causing an outage, because new pods must be healthy before old ones are removed, and `rollout undo` restores the last good version in one command."

### 5:30 - 6:00  Wrap-up

*Say:* "In short: four independent microservices with a database each, one GKE cluster, autoscaling, replicas and a managed load balancer, and zero-downtime rollouts with rollback. Monthly cost is about US$110 on a 24/7 estimate."

---

## 3. If you are running late (what to cut)

1. Cut the rollout of the analyzer v2 first, and keep only the broken-release rollback.
2. Cut the Overview and run explorer from the app tour.
3. Never cut the autoscaler view or the rollback: they carry the marks.

## 4. If something goes wrong

| Problem | What to do |
|---|---|
| Site returns 502 | Wait 1-2 minutes; the load balancer is re-checking pods. |
| The autoscaler hasn't scaled | Say "it checks every 15 s" and show the earlier screenshot or the backup video. |
| `kubectl` can't connect | `gcloud container clusters get-credentials flaketracker --zone australia-southeast2-b` |
| Cluster won't start at all | Play the **backup video**. (The local Docker Compose stack runs the same app, but a local-only demo loses marks.) |
| A dashboard page shows an error | Say "only that service's data is unavailable; the rest of the app is up": that is the failure isolation. |

## 5. Likely questions (about 2 minutes)

- **Why a database per service?** Each service can scale, fail and change its schema independently; they share data only through HTTP APIs.
- **What if a database goes down?** Only the endpoints that need it return a 503; the rest of the dashboard keeps working. We drained a node with a database on it and it came back in about 30 seconds.
- **How does the analyzer avoid doing work twice with two replicas?** A PostgreSQL advisory lock elects one leader; the other waits.
- **Why is ingest safe to retry?** A run's identity is unique, so a duplicate returns "200 duplicate" and nothing is written twice.
- **Why not Cloud SQL?** In-cluster PostgreSQL is much cheaper for three small databases; the trade-off is no managed backups or automatic failover.
- **Is the data real?** It is generated (seeded) so the demo is repeatable; it enters through the same ingest API a real CI pipeline would use.
- **Why HTTP and not HTTPS?** A managed certificate needs a domain name; in production we would terminate TLS at the load balancer.
- **Do the nodes scale automatically?** Pods do. The node pool is a fixed three; a node autoscaler is the next step.

## 6. After the demo (put it all back)

```bash
# Put the analyzer back on v1 so the next run starts the same way.
kubectl -n flaketracker set image deployment/analyzer analyzer=$REGISTRY/analyzer:oct20-1
kubectl -n flaketracker annotate deployment/analyzer kubernetes.io/change-cause="back to v1" --overwrite

# Stop the VMs so the cluster stops costing money (the app, its data and the load balancer stay).
ASSUME_YES=1 scripts/gcp/scale-down.sh
```
