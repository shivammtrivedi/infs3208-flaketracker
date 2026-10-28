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
- **Is the data real?** It is generated (seeded) so the demo is repeatable; it enters through the same ingest API a real CI pipeline would use. Full answer in section 8; where each feature is in the code is section 7.
- **Why HTTP and not HTTPS?** A managed certificate needs a domain name; in production we would terminate TLS at the load balancer.
- **Do the nodes scale automatically?** Pods do. The node pool is a fixed three; a node autoscaler is the next step.

## 6. Deployment choices: what we need, why we chose it, what it costs

**The 20-second answer if you are asked "why these choices?":** *"Each choice is the cheapest option that still lets us show every requirement: scalability, reliability, load balancing, orchestration and rollouts. The 24/7 list price is about US$185 a month, and about US$110 after Google's free-tier credit for one zonal cluster."*

Costs are from the Google Cloud Pricing Calculator (Melbourne, 24/7). Re-check them before you quote them.

### Cluster, zone and machines

| Choice | What we need | Why this one | Cost (US$ / month) |
|---|---|---|---|
| **GKE Standard** (not Autopilot, not Cloud Run) | To show nodes, drain a node, run databases on disks, and use Ingress and autoscalers directly | Standard exposes the nodes and the full Kubernetes API. Autopilot and Cloud Run hide the nodes, so we couldn't demonstrate draining or spreading across nodes | Cluster fee 73, covered by the free tier for one zonal cluster = about 0 |
| **One cluster** | One environment to run and demo | A second cluster would double the nodes and the fee, and nothing in the requirements needs it | (one fee only) |
| **Zonal** (`australia-southeast2-b`), not regional | Low cost; one zone is enough for a demo | A regional cluster puts nodes in 3 zones (3x the VMs) and loses the free-tier credit. The trade-off is a zone outage would take us down, so we say so openly | 0 extra |
| **Region: Melbourne** (`australia-southeast2`) | Low latency for Australian users and the course | Closest region to us. Pricing differs by region, and this was checked in the calculator | (in the rows below) |
| **3 nodes** | Drain one node and still have two to spread replicas across | Two nodes would leave a single node during a drain, and one node would show no spreading at all | 3 x 29.50 = 88.50 |
| **e2-medium** (2 shared vCPU, 4 GB) | Enough room for our 11 pods (8 service pods and 3 databases) plus GKE's own system pods | GKE's system pods take about 450m CPU on every node, leaving about 940m usable on an e2-medium, so a smaller machine would have very little room. A bigger one costs more and isn't needed | included above |
| **On-demand, not Spot** | A machine that stays up during the marked demo | Spot is about half the price (US$0.0202 vs 0.0404 an hour) but can be reclaimed at any time, and marks are lost for demo disruptions. We use Spot only for non-demo work | included above |
| **30 GB balanced disks** | A normal boot disk | pd-balanced is the standard default, cheaper than SSD and faster than standard; 30 GB leaves room for images | about 12 for 3 |
| **Fixed node pool, no cluster autoscaler** | Predictable cost | Pods autoscale, nodes don't. The node pool is a fixed 3, so the bill can't surprise us. A node autoscaler is the stated next step | 0 |
| **Regular release channel** | Patched Kubernetes without manual upgrades | Google keeps the version current on the standard schedule | 0 |
| **System-only logging and monitoring** | Our own logs read with `kubectl logs` | Sending every application log line to Cloud Logging adds a charge we don't need | about 0 |

### Traffic

| Choice | What we need | Why this one | Cost (US$ / month) |
|---|---|---|---|
| **GKE Ingress, which creates a Google external HTTP(S) load balancer** | One public address that routes `/`, `/api` and `/ingest` to different services, with health checks | A `Service` of type LoadBalancer is layer 4 only and would need one load balancer, and one cost, per service, with no path routing. Ingress gives path routing behind a single forwarding rule | 18.58 |
| **Reserved global static IP** (`flaketracker-ip`) | An address that never changes, for the README, load test and demo | A reserved address survives re-creating the Ingress. It's free while attached to the load balancer | 0 while in use |
| **Health checks on `/healthz`** | The load balancer only sends traffic to healthy pods | Pods that aren't ready are left out, so users never see a pod that is still starting | 0 |
| **HTTP only, no HTTPS** | Not required by the course | A managed certificate needs a domain name, and a second forwarding rule would add about US$18. Stated as a known limit | 0 |

### Scaling, reliability and releases

| Choice | What we need | Why this one | Cost (US$ / month) |
|---|---|---|---|
| **Horizontal Pod Autoscaler on `ingest` (2 to 8) and `api` (2 to 6), scaling at 60% CPU** | Handle bursts of CI results and dashboard traffic | These two services get uneven load. CPU is built in, needs no extra tools and is simple to demo. Ingest was seen scaling from 2 to 5 pods in under 40 seconds | 0 (uses existing nodes) |
| **No autoscaler on `analyzer` and `web`** | Don't waste capacity | The analyzer elects one leader to do the work, so more copies wouldn't make it faster. `web` serves small static files | 0 |
| **At least 2 replicas of every service** | Survive a pod or node failure | One replica would mean an outage whenever a pod restarts. Spread across nodes by topology rules | 0 (small requests fit) |
| **Small CPU requests (50m, web 10m) with limits kept** | Fit everything on 3 small nodes and let autoscaling work | The autoscaler measures CPU against the request. Larger requests filled the nodes (a drained database pod stayed Pending), so we lowered them. Limits still allow bursts | 0 |
| **Pod disruption budgets (minimum 1) on stateless services** | A drain or upgrade never removes the last copy | They protect availability during maintenance. Not on the single-replica databases, because a budget there would block a drain forever | 0 |
| **Rolling updates, maxUnavailable 0 and maxSurge 1** | Release with no downtime, and a bad release never replaces working pods | Old pods stay until the new one is healthy. The cost is one extra pod for a short time | 0 |
| **Images tagged with the Git commit** (never `latest`) | Know exactly what runs, and roll back reliably | `latest` can't be traced or rolled back. A commit tag can | 0 |
| **Distroless Go images, nginx for the front end, in Artifact Registry** | Small, quick-to-pull images with little attack surface | About 20 MB each, so pods start fast. Artifact Registry is in the same region | about 0.05 |

### Data

| Choice | What we need | Why this one | Cost (US$ / month) |
|---|---|---|---|
| **PostgreSQL 17, one database per service** | Constraints, uniqueness (for safe retries) and joins, plus true microservice independence | A relational database enforces the rules we need. Separate databases mean one service's data or failure can't break another's | see disks below |
| **Databases inside the cluster (StatefulSets), not Cloud SQL** | A low bill | Three managed instances would cost many times more than three small disks. The trade-off is no managed backups or automatic failover, which we state openly | 0.54 for 8 GiB of disks |
| **Persistent disks sized 5, 2 and 1 GB** | Hold the data with headroom | Matched to what each database stores (about 50 MB of seeded data at most), so we don't pay for unused space | included above |
| **Single replica per database, no pod disruption budget** | Keep it simple and cheap | Replicated Postgres adds complexity and cost. A drained database pod moved to another node and was ready again in about 33 seconds | 0 |

### Totals

| | US$ / month |
|---|---|
| GKE (cluster fee, 3 nodes, boot disks) | 165.54 (calculator, with 10 GiB boot disks; about 8 more at 30 GiB) |
| Load balancer | 18.58 |
| Database disks | 0.54 |
| Artifact Registry | 0.05 |
| **List-price total** | **184.71** (about 193 at 30 GiB boot disks) |
| Less GKE free-tier credit | -74.40 |
| **Estimated bill** | **about 110** (about 118 at 30 GiB) |

Cost controls: the nodes scale to zero outside working hours (`scale-down.sh`), which cuts the real bill to roughly US$40 a month, and Spot would roughly halve the node cost for non-demo use.

## 7. Where each feature lives in the code (if the tutor says "show me")

Paths are from the repo root. Function names are given instead of line numbers because line
numbers drift; in VS Code press `Ctrl+P`, type the file name, then `Ctrl+Shift+O` and type the function name.

The backend is Go (`cmd/` = the program that starts each service, `internal/` = the logic).
The dashboard is React + TypeScript (`web/src/`). Database tables are in `db/migrations/`.

### The five features the dashboard shows

| Feature | What it does (one sentence) | Backend logic | Dashboard page |
|---|---|---|---|
| **F1 Overview** | Shows the totals: tests, how many are flaky, broken or stable, runs, quarantined, and a trend over time. | `internal/api/handlers.go` `summary` (joins the three services' answers); `internal/analyzer/api.go` `Summary`; `internal/ingest/read.go` `Summary`; trend data is written by `internal/analyzer/cycle.go` `refreshDaily` into table `daily_flaky_counts` | `web/src/pages/Overview.tsx` |
| **F2 Leaderboard** | Lists tests ranked by flakiness score, with filters, sorting and paging. | `internal/api/handlers.go` `listTests` (adds the quarantined flag); `internal/analyzer/api.go` `ListTests` (filter, sort and paging SQL) | `web/src/pages/Leaderboard.tsx`, filters kept in the URL by `web/src/useUrlFilters.ts` |
| **F3 Test detail and history** | One test's score, its signals, its quarantine status and its last results over time. | `internal/api/handlers.go` `getTest` and `history`; `internal/ingest/read.go` `Test` and `History` | `web/src/pages/TestDetail.tsx` |
| **F4 Quarantine** | Lets a person park a flaky test with a reason, list the parked tests, and release one. | `internal/api/handlers.go` `createQuarantine`, `listQuarantines`, `releaseQuarantine`; `internal/quarantine/store.go` (`Create`, `Release`, `Active`, `ActiveFor`, `CountActive`); table `quarantines` in `db/migrations/api/000001_init.up.sql` | `web/src/pages/Quarantine.tsx` (the list and Release); the Quarantine button is in `web/src/pages/TestDetail.tsx`; calls are in `web/src/api/hooks.ts` |
| **F5 Run explorer** | Lists CI runs and shows each run's test results and retries. | `internal/api/handlers.go` `listRuns` and `getRun`; `internal/ingest/read.go` `Runs` and `Run`; index in `db/migrations/ingest/000002_run_explorer_index.up.sql` | `web/src/pages/Runs.tsx`, `web/src/pages/RunDetail.tsx` |

If asked "where is the quarantine implemented?": the rules are in `internal/api/handlers.go`
(`createQuarantine` checks the reason, asks ingest if the test exists, then saves), the saving
is in `internal/quarantine/store.go`, and "a test can only be quarantined once at a time" is
enforced by the database itself with the partial unique index `uq_active_quarantine`
(`db/migrations/api/000001_init.up.sql`). The quarantine is a record in `api-db`; nothing in CI
reads it yet, a pipeline would call `GET /api/v1/quarantine?repo=...` to get the list to skip.

### The engine behind the features

| Part | What it does | Files |
|---|---|---|
| **Ingestion** (the backbone) | Accepts a CI run as JSON, checks it, saves the run and all its test results in one transaction; a repeat of the same run is safe. | `cmd/ingest/main.go` (routes, API key check at startup); `internal/ingest/handler.go` `NewHandler` (5 MB cap, 400 with all problems, 202 new, 200 duplicate); `internal/ingest/payload.go` `Validate` and `DeriveResult` (final status, passed-on-retry); `internal/ingest/store.go` `Insert` (one transaction, `ON CONFLICT DO NOTHING`); `internal/httpx/httpx.go` `RequireAPIKey`; unique key `(repository_id, ci_run_id, ci_run_attempt)` in `db/migrations/ingest/000001_init.up.sql` |
| **Feed from ingest to analyzer** | Hands the analyzer new results in order, never skipping one. | `internal/ingest/feed.go` `Feed` and `NewFeedHandler` (the 10 s lag); `internal/clients/ingest.go` |
| **Flakiness scoring** | Turns a test's last 30 results into a score and a verdict: stable, flaky, broken or not enough data. v1 counts all results equally, v2 lets old results fade. | `internal/scoring/scoring.go` (`V1`, `V2`, `score`, `isBroken`); unit tests in `internal/scoring/scoring_test.go`; thresholds in `deploy/k8s/base/01-configmap.yaml` |
| **Analyzer loop and leader election** | Every 15 s one elected replica pulls new results, re-scores the changed tests and saves them. | `internal/analyzer/analyzer.go` (`Run`, `tick`, `lead` = Postgres advisory lock); `internal/analyzer/cycle.go` (`Cycle`, `rescore`); `cmd/analyzer/main.go` |
| **API gateway** | The one backend the dashboard talks to; it calls ingest and analyzer over HTTP and combines the answers. | `cmd/api/main.go`; `internal/api/server.go` (all routes in `Register`); `internal/clients/` (HTTP clients with timeouts and retries) |
| **Failure isolation** | If one downstream service is down, only the endpoints that need it answer 503. | `internal/api/server.go` `downstreamError`; the `Degraded` field in `internal/api/handlers.go` |
| **Health checks and shutdown** | Liveness (`/healthz`) and readiness (`/readyz`), and finishing in-flight requests on shutdown. | `internal/httpx/httpx.go` `Healthz`, `Readyz`; shutdown code at the end of each `cmd/*/main.go` |
| **Demo data generator** | Makes the 450 tests and their history, then sends them through the real ingest API. | `cmd/seed/main.go`; `internal/seed/generate.go` `Generate`; `internal/seed/api.go` `PostAPI` |
| **Database migrations** | Creates each service's own tables before the service starts (a Kubernetes Job). | `cmd/migrate/main.go`; `db/migrations/{ingest,analyzer,api}/`; `deploy/k8s/base/20-migrate-jobs.yaml` |

### The deployment pieces

| Part | Files |
|---|---|
| The four services and three databases as Kubernetes objects (Deployments, StatefulSets, Services) | `deploy/k8s/base/10-` to `33-*.yaml` |
| Autoscaling (HPA) | inside `deploy/k8s/base/30-ingest.yaml` (2 to 8 pods) and `31-api.yaml` (2 to 6 pods) |
| Disruption budgets (PDB, keep at least one copy up during upgrades and node drains) | inside `30-ingest.yaml`, `31-api.yaml`, `32-analyzer.yaml` and `33-web.yaml` |
| Shared settings (scoring thresholds, intervals) | `deploy/k8s/base/01-configmap.yaml` |
| Load balancer routing: `/api` to api, `/ingest` to ingest, everything else to web; the fixed public IP | `deploy/k8s/overlays/gke/ingress.yaml` |
| Load balancer health checks (`BackendConfig`) | `deploy/k8s/overlays/gke/backendconfigs.yaml` |
| Container images | `build/go.Dockerfile` (one file builds all three Go services; the `SERVICE` build argument picks which) and `web/Dockerfile` (dashboard + nginx); built and pushed by `scripts/gcp/build-push.sh` |
| Start and stop the cluster | `scripts/gcp/scale-up.sh`, `scripts/gcp/scale-down.sh` |
| Load test used for the autoscaling demo | `scripts/loadtest/ingest-load.sh` |

---

## 8. Could FlakeTracker use real CI data from a real GitHub repo?

**Short answer (say this):** Yes, the receiving side is built and is not tied to fake data.
The demo data is generated, but it goes in through the same public ingest API a real pipeline
would use. What we have **not** built is the sending side: a small step in a GitHub Actions
workflow that turns the test results into our JSON and posts it. That is the missing piece,
and it is a small one.

### What is built and what is not

| Built (works today) | Not built (would be added) |
|---|---|
| `POST /ingest/v1/runs` on the public address, protected by an API key | A step in the repo's GitHub Actions workflow that sends the results |
| The JSON format and checks (`internal/ingest/payload.go`) | A converter from the test tool's output (for example JUnit XML) to our JSON |
| Safe retries: the same run sent twice is stored once | Running the cluster all the time (it is scaled to zero when not in use, so a pipeline would get no answer) |
| Scoring, leaderboard, quarantine on top of whatever is ingested | HTTPS (the key is sent over plain HTTP today) |
| The field names already match GitHub: the table comment in `db/migrations/ingest/000001_init.up.sql` says `ci_run_id` is `GITHUB_RUN_ID` and `ci_run_attempt` is `GITHUB_RUN_ATTEMPT` | A way for CI to skip quarantined tests (it would call `GET /api/v1/quarantine`) |

### How it would work, step by step

1. **The workflow runs the tests** and writes a result file. Most test tools can write JUnit
   XML. To see flakiness inside one run the tool must retry failed tests (for example
   `gotestsum --rerun-fails`, pytest with `pytest-rerunfailures`, or Jest/Playwright retries).
2. **A converter step** reads that file and builds our JSON: one entry per test, and for each
   test every attempt in order (`passed`, `failed` or `skipped`, plus duration and error message).
   Tools record reruns differently, so this converter is written per test tool.
3. **The same step fills in the run details from GitHub's built-in variables:**

   | Our JSON field | GitHub Actions value |
   |---|---|
   | `repository` | `$GITHUB_REPOSITORY` (like `owner/name`) |
   | `ci_run_id` | `$GITHUB_RUN_ID` |
   | `ci_run_attempt` | `$GITHUB_RUN_ATTEMPT` (goes up by one when someone clicks "Re-run jobs") |
   | `branch` | `$GITHUB_REF_NAME` |
   | `commit_sha` | `$GITHUB_SHA` |
   | `run_url` | `$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID` |
   | `started_at` | the time the job started |

4. **The step posts it** with `curl` to `http://<the public IP>/ingest/v1/runs`, with the header
   `X-API-Key`. The key is stored as a GitHub **secret**, never written in the workflow file.
   GitHub's runners are on the public internet, so they can reach the load balancer.
5. **FlakeTracker does the rest.** Ingest saves it, the analyzer picks it up within about 25
   seconds (10 s feed lag + 15 s loop), and the test shows on the dashboard.

### What a tutor might ask next

- **Would it find flaky tests straight away?** No. A test needs at least 5 results before it is
  judged (`MIN_SAMPLES`), and the score looks at its last 30. Until then it shows as
  "insufficient data". A real repo needs a few days of normal pushes, or the old runs loaded in.
- **Can it read the past history of a repo?** Not through GitHub's API alone. The API gives pass
  or fail per job, not per test, so per-test results exist only in the test report. A backfill
  would download the saved report files from old workflow runs and post each one. We did not build that.
- **What counts as flaky in real data?** The same two signals the seeded data uses: a test that
  failed and then passed on retry in the same run, and a test that both passed and failed on the
  same commit (a "Re-run jobs" click gives the same commit with a new attempt number).
- **Is it safe to open ingest to the internet?** The key protects it, the body is capped at
  5 MB, and every field is checked. The weak points are HTTP instead of HTTPS (the key could be
  read on the way) and one shared key for every repo. In production: HTTPS on the load balancer,
  one key per repo, and rate limiting.
- **Does quarantine stop the flaky test in CI?** Not yet. The quarantine is a record. The
  pipeline would call `GET /api/v1/quarantine?repo=owner/name` and skip those tests.
- **Why did you use generated data then?** A real repo would take days or weeks to build up
  history, and the demo has to be repeatable. Generated data lets us show every verdict (flaky,
  broken, stable) and the v1 to v2 change on demand.

### Optional live proof (only if you want to show it)

This sends one made-up run in the exact format a real pipeline would send, so you can show
that the same endpoint accepts it. It **adds one run and one test to the live data** (450 tests
becomes 451) and there is no delete button, so do it **after** the numbers have been shown, or skip it.

```bash
# Send one run, in the real format, to the public ingest endpoint.
#   -s                  silent (no progress bar)
#   -X POST             the HTTP method; this endpoint only accepts POST
#   -H 'X-API-Key: ..'  the secret from the KEY variable set in section 1
#   -H 'Content-Type..' tells the server the body is JSON
#   -d '...'            the body; ci_run_id uses the clock so it is unique each time
#                       (sending the same id twice returns 200 "duplicate" instead)
curl -s -X POST http://34.96.64.67/ingest/v1/runs \
  -H "X-API-Key: $KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "repository": "demo/live-ci",
    "ci_run_id": "'"$(date +%s)"'",
    "ci_run_attempt": 1,
    "run_url": "https://github.com/demo/live-ci/actions/runs/1",
    "branch": "main",
    "commit_sha": "0123456789abcdef0123456789abcdef01234567",
    "started_at": "'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'",
    "results": [
      {"suite": "checkout", "name": "applies_discount_code",
       "attempts": [{"status": "failed", "duration_ms": 420, "error": "timeout"},
                    {"status": "passed", "duration_ms": 380, "error": ""}]}
    ]
  }'
# Expect: {"results":1,"run_id":...}  (HTTP 202). Open the Runs page: the new run is at the top.
# The test shows as "insufficient data" because it has only one result so far.
```

---

## 9. After the demo (put it all back)

```bash
# Put the analyzer back on v1 so the next run starts the same way.
kubectl -n flaketracker set image deployment/analyzer analyzer=$REGISTRY/analyzer:oct20-1
kubectl -n flaketracker annotate deployment/analyzer kubernetes.io/change-cause="back to v1" --overwrite

# Stop the VMs so the cluster stops costing money (the app, its data and the load balancer stay).
ASSUME_YES=1 scripts/gcp/scale-down.sh
```
