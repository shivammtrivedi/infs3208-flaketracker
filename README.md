# FlakeTracker

A cloud-native web app that ingests CI test results, detects flaky tests, and gives engineers a dashboard to triage and quarantine them.

## Run with Docker Compose (recommended)

This runs the whole system on your machine the way it will run in the cloud: three databases (one per service), two copies of each service, and a proxy that sends `/api/` and `/ingest/` to the right place. The only thing you need installed is **Docker Desktop** (or Docker Engine with the Compose plugin); Go and Node are used only inside the image builds.

```bash
# 1. Go to the Compose folder. Every command below is run from here.
cd deploy/compose

# 2. Build the images and start everything in the background.
#    up -d     start the containers and return (-d = detached, in the background)
#    --build   build the images from the source in this repo first
#    The first build takes a few minutes; later builds reuse cached layers.
docker compose up -d --build

# 3. Check that everything is healthy. The three "migrate-*" containers should show
#    "Exited (0)": they create the database tables once and stop, which is correct.
docker compose ps

# 4. Load 135,000 demo test results into ingest's database.
#    --profile tools   also enable the "tools" services (the seed generator)
#    run --rm          run it once, then delete the container
#    --truncate        empty ingest's tables first (safe to repeat)
docker compose --profile tools run --rm seed --mode=db --truncate

# 5. Open http://localhost:8080 in your browser. Give it about 15 seconds after
#    step 4: the analyzer has to pull and score the new data before the numbers appear.
```

To stop: `docker compose down` keeps your data (it lives in Docker volumes); `docker compose down -v` also deletes the data.

### Things to try (testing locally, before deploying anywhere)

Each of these is something the cloud deployment will do too, so it is worth seeing it work here first.

```bash
# --- See that it really is several services -------------------------------------------
# One line per container. Note two copies each of ingest, api, analyzer and web, and
# that only the proxy publishes a port (8080); the databases are not reachable from outside.
docker compose ps

# --- Send test results the way a CI pipeline would ------------------------------------
# POST one run to ingest through the proxy.
#   -X POST            send a POST request
#   -H 'X-API-Key: …'  the key ingest requires (INGEST_API_KEY, default "ingestkey")
#   -d '{…}'           the JSON body (the format is in the project plan, Section 9.1)
# Run it twice: the first answer is 202, the second is 200 with "duplicate": true.
curl -X POST http://localhost:8080/ingest/v1/runs -H 'X-API-Key: ingestkey' \
  -d '{"repository":"acme/demo","ci_run_id":"1","branch":"main","commit_sha":"abc123","started_at":"2026-10-17T10:00:00Z","results":[{"suite":"a.spec.ts","name":"works","attempts":[{"status":"passed","duration_ms":5}]}]}'

# --- Failure isolation: stop one service, the rest keeps working ----------------------
# Stop both analyzer copies, then refresh the dashboard: the Leaderboard shows an
# "unavailable" message and the Overview shows a banner, but Runs and Quarantine still work.
docker compose stop analyzer
# Bring it back: the pages recover by themselves within seconds.
docker compose start analyzer

# --- Reliability: kill a service copy while using the app -----------------------------
# Click around the dashboard, run this in another terminal, and nothing breaks:
# the proxy sends traffic to the surviving copy. (Docker does not restart a container
# you kill by hand; "docker compose up -d" brings the missing copy back.)
docker kill flaketracker-api-1
docker compose up -d

# --- Leader election: only one analyzer computes --------------------------------------
# Two analyzers run, but only one (the leader) scores tests; the other stands by.
docker compose logs analyzer | grep -E "became leader|standing by"
# Kill the leader (use the container named in the log) and the other takes over in a few seconds.
docker compose logs --since 15s analyzer | grep "cycle complete"

# --- Load balancing: requests are split between the two copies ------------------------
# Send 20 requests, then see that both api copies logged some of them.
for i in $(seq 1 20); do curl -s -o /dev/null http://localhost:8080/api/v1/repos; done
docker compose logs api | grep -c '"path":"/api/v1/repos"'

# --- Rollout and rollback, rehearsed locally ------------------------------------------
# Scoring algorithm v2 is a different build of the analyzer. Rebuild it as v2 (takes a
# couple of minutes) and watch the dashboard: the footer changes to "algorithm v2" and the
# number of flaky tests drops from 74 to 50, because recent results count for more.
ANALYZER_ALGORITHM=v2 docker compose up -d --build analyzer
# Roll back by rebuilding the default (v1): the footer and the numbers return.
docker compose up -d --build analyzer

# --- Follow one request through all three services ------------------------------------
# Every request carries an ID (X-Request-ID) that each service logs. Send one with a
# name you choose, then find it in the logs of api, analyzer and ingest.
curl -s -o /dev/null -H 'X-Request-ID: demo-1' http://localhost:8080/api/v1/summary
docker compose logs | grep demo-1

# --- The data survives a restart ------------------------------------------------------
docker compose down       # stop and remove the containers; the Docker volumes stay
docker compose up -d      # start again: the data is still there, no need to re-seed
```

Optional settings (database passwords, the analyzer interval, build info) are described in [deploy/compose/.env.example](deploy/compose/.env.example). If something does not start, `docker compose logs <service>` shows why; port 8080 already being in use is the most common cause.

## Rehearse the Kubernetes deployment locally (free)

Before spending any cloud credit, run the real Kubernetes manifests on a local cluster. [Kind](https://kind.sigs.k8s.io) runs a Kubernetes cluster inside Docker: here one control-plane node and three workers, like the three-node GKE cluster. Prerequisites: Docker, `kubectl` and `kind` (`go install sigs.k8s.io/kind@v0.29.0`). The scripts are bash (Git Bash, WSL, macOS, Linux).

```bash
# 1. Create the cluster, build the images, load them into the cluster, install
#    metrics-server (the autoscalers need it) and deploy. Takes about 5 minutes the first time.
scripts/k8s/local-rehearsal.sh

# 2. Load the 135,000-result demo data. This runs the seed tool as a Job INSIDE the
#    cluster, so the database is never exposed.
scripts/k8s/seed.sh local

# 3. Open the app. This forwards a local port to the "edge" proxy that stands in for the cloud Ingress.
#    (Use another local port if 8080 is taken, e.g. by Docker Compose: 8081:80.)
kubectl -n flaketracker port-forward svc/edge 8080:80
#    then open http://localhost:8080

# See everything that is running, and which node each pod landed on.
kubectl -n flaketracker get pods -o wide

# Delete a pod while using the app: Kubernetes replaces it and nothing breaks.
kubectl -n flaketracker delete pod -l app=api --wait=false

# Remove the whole local cluster when finished.
kind delete cluster --name flaketracker
```

The one thing this cannot test is the Google Ingress / load balancer, which exists only on GKE.

## Deploy to Google Cloud (GKE)

Run these from Google Cloud Shell (everything is preinstalled) or from a machine with `gcloud`, `kubectl` and Docker. Roughly USD 4-5 per day while the three nodes are running. Each script prints the account and project it will use, and asks before it creates anything.

```bash
# 0. Check which Google account and project gcloud is using. Fix it with
#    "gcloud config set account ..." and "gcloud config set project ..." if it is wrong.
gcloud config list

# 1. One-time setup: enable the APIs, create the image registry, reserve the static IP.
scripts/gcp/setup-project.sh

# 2. Create the cluster (3 x e2-medium nodes, zone australia-southeast2-b). Takes about 5 minutes.
#    It also installs gke-gcloud-auth-plugin if missing: kubectl needs that helper to log in to
#    GKE with your Google account. (SPOT=1 makes the nodes cheaper Spot VMs, NODES=2 uses two.)
scripts/gcp/create-cluster.sh

# 3. Build the images and push them to the registry, tagged with the current git commit.
#    It prints the tag (e.g. d5dc236); use that tag below.
scripts/gcp/build-push.sh

# 4. Create the Secrets (random database passwords and the ingest API key).
scripts/k8s/create-secrets.sh

# 5. Deploy, in stages: databases, then migrations, then the services.
#    Replace <TAG> with the tag printed in step 3.
scripts/k8s/deploy.sh gke <TAG>

# 6. Load the demo data (runs inside the cluster).
scripts/k8s/seed.sh gke <TAG>

# 7. Find the public address. The Google load balancer is slow to build: allow 10-20 minutes
#    (it took about 16 the first time) before the address answers. The ADDRESS is the reserved
#    static IP from step 1. Open http://<ADDRESS> in a browser once "curl -I http://<ADDRESS>/healthz" returns 200.
kubectl -n flaketracker get ingress flaketracker
```

### Autoscaling and failure drills

```bash
# Get the ingest API key, then generate load: 24 parallel workers for 2 minutes. The requests replay an
# existing run, so ingest does real work (decode + validate) but no data is changed.
KEY=$(kubectl -n flaketracker get secret ingest-api-key -o jsonpath='{.data.api-key}' | base64 -d)
scripts/loadtest/ingest-load.sh http://<ADDRESS> "$KEY" 24 120

# In another terminal, watch the autoscaler add pods (it went from 2 to 5 replicas in under 40 seconds)
# and remove them again about 2.5 minutes after the load stops.
kubectl -n flaketracker get hpa ingest -w

# Delete a pod while the dashboard is in use: Kubernetes replaces it, nothing breaks.
kubectl -n flaketracker delete pod -l app=ingest --wait=false

# Drain a node (move every pod off it, as before maintenance). The disruption budgets keep at least one
# copy of each service running; a database on that node moves and is ready again in about 30 seconds.
kubectl get nodes
kubectl drain <NODE_NAME> --ignore-daemonsets --delete-emptydir-data
kubectl uncordon <NODE_NAME>      # let it take pods again afterwards
```

### Rollout and rollback

```bash
# Build the analyzer with scoring algorithm v2 (a different image, not a config switch).
SERVICES=analyzer ALGORITHM=v2 TAG=<TAG>-v2 scripts/gcp/build-push.sh

# Record WHY, then switch the running Deployment to the v2 image. Annotate BEFORE set image:
# Kubernetes copies the annotation onto the new revision as it is created.
kubectl -n flaketracker annotate deployment/analyzer kubernetes.io/change-cause="v2: recency-weighted scoring" --overwrite
REGISTRY=australia-southeast2-docker.pkg.dev/$(gcloud config get-value project)/flaketracker
kubectl -n flaketracker set image deployment/analyzer analyzer=$REGISTRY/analyzer:<TAG>-v2

# Watch the pods being replaced one at a time with no downtime (maxUnavailable: 0).
# The dashboard footer changes to "algorithm v2" and the flaky count drops from 74 to 50.
kubectl -n flaketracker rollout status deployment/analyzer

# Roll back. Undo FIRST, then annotate: the other order mislabels the broken revision in the history.
kubectl -n flaketracker rollout undo deployment/analyzer
kubectl -n flaketracker annotate deployment/analyzer kubernetes.io/change-cause="rolled back to v1" --overwrite

# The list of revisions and why each was made.
kubectl -n flaketracker rollout history deployment/analyzer

# A broken release stalls instead of causing an outage: point a Deployment at an image that
# does not exist. The new pod sits in ImagePullBackOff and the old pods keep serving.
kubectl -n flaketracker set image deployment/api api=$REGISTRY/api:does-not-exist
kubectl -n flaketracker get pods -l app=api
kubectl -n flaketracker rollout undo deployment/api
```

### Saving credit, and cleaning up

```bash
# Between working sessions: stop the application, then delete the three VMs, keeping the cluster, the
# load balancer and the database disks (the bulk of the cost stops; about USD 0.7 a day remains).
# It stops the workloads FIRST, because letting GKE delete nodes under running pods deadlocks on the
# disruption budgets while the VMs keep billing.
scripts/gcp/scale-down.sh
# Start it all again: nodes, then the databases (data intact), then the services (about 5 minutes).
scripts/gcp/scale-up.sh

# When completely finished: delete everything this project created (the data is lost).
scripts/gcp/teardown.sh
```

## Run each piece by hand (for development)

Prerequisites: Go, Node.js and Docker. Commands are bash (Git Bash, WSL, macOS or Linux). Use this when you are changing code and want fast restarts; otherwise prefer Docker Compose above.

Each service owns its own database, so there are three Postgres instances and every service is pointed at *its own* one with the `DB_*` variables.

```bash
# 1. Start one Postgres per service.
#    --rm            delete the container when it stops
#    -d              run in the background
#    --name          the container's name
#    -e POSTGRES_*   create this user, password and database on first start
#    -p HOST:5432    publish Postgres on this port of your machine (5432, 5433, 5434)
docker run --rm -d --name ft-ingest-db   -e POSTGRES_USER=ingest   -e POSTGRES_PASSWORD=ingestdb -e POSTGRES_DB=ingest   -p 5432:5432 postgres:17
docker run --rm -d --name ft-analyzer-db -e POSTGRES_USER=analyzer -e POSTGRES_PASSWORD=analyzerdb -e POSTGRES_DB=analyzer -p 5433:5432 postgres:17
docker run --rm -d --name ft-api-db      -e POSTGRES_USER=api      -e POSTGRES_PASSWORD=apidb -e POSTGRES_DB=api      -p 5434:5432 postgres:17

# 2. Create each service's tables.
#    DB_PORT / DB_NAME / DB_USER / DB_PASSWORD   which database to connect to (DB_HOST defaults to localhost)
#    --service                                   which service's migrations to apply (db/migrations/<service>)
DB_PORT=5432 DB_NAME=ingest   DB_USER=ingest   DB_PASSWORD=ingestdb go run ./cmd/migrate --service=ingest
DB_PORT=5433 DB_NAME=analyzer DB_USER=analyzer DB_PASSWORD=analyzerdb go run ./cmd/migrate --service=analyzer
DB_PORT=5434 DB_NAME=api      DB_USER=api      DB_PASSWORD=apidb go run ./cmd/migrate --service=api

# 3. Load 135,000 demo test results into ingest's database (see "Seed generator" below).
DB_PORT=5432 DB_NAME=ingest DB_USER=ingest DB_PASSWORD=ingestdb go run ./cmd/seed --mode=db

# 4. Start the three services, each in its own terminal.
#    PORT                 the port this service listens on
#    INGEST_API_KEY       the key CI must send in the X-API-Key header
#    INGEST_URL           where to reach ingest's internal API
#    ANALYZER_URL         where to reach the analyzer's internal API
#    ANALYZER_INTERVAL    how often the analyzer scores tests (5s so the demo data appears quickly)
PORT=8090 DB_PORT=5432 DB_NAME=ingest   DB_USER=ingest   DB_PASSWORD=ingestdb INGEST_API_KEY=ingestkey go run ./cmd/ingest
PORT=8091 DB_PORT=5433 DB_NAME=analyzer DB_USER=analyzer DB_PASSWORD=analyzerdb INGEST_URL=http://localhost:8090 ANALYZER_INTERVAL=5s go run ./cmd/analyzer
PORT=8092 DB_PORT=5434 DB_NAME=api      DB_USER=api      DB_PASSWORD=apidb INGEST_URL=http://localhost:8090 ANALYZER_URL=http://localhost:8091 go run ./cmd/api

# 5. Start the dashboard (from web/) and open http://localhost:5173.
#    npm install installs the dependencies; npm run dev serves the app and proxies /api to the api service.
cd web && npm install && npm run dev
```

The first analysis takes about six seconds; the dashboard fills in after that. See [web/README.md](web/README.md) for the dashboard's pages and code layout.

## Seed generator (`cmd/seed`)

Generates deterministic demo data: by default 3 repositories × 150 tests × 300 CI runs over the last 60 days = **135,000 test results**. The mix is exactly 85% stable, 10% flaky, 3% broken and 2% recently fixed. Runs include retries, same-commit re-runs, feature branches, skipped tests and timeout spikes.

It loads data into the **ingest** service only. Under database-per-service, the analyzer picks the data up through ingest's results feed, the same way it handles live CI data.

### Modes

| Mode | What it does | When to use it |
|---|---|---|
| `--mode=db` | Bulk-inserts straight into **ingest-db** in one transaction (Postgres `COPY`). ~3 s for 135k results. | Fast initial load |
| `--mode=api` | POSTs every run to `POST /ingest/v1/runs`, exactly like a CI pipeline. Retries are safe because ingest is idempotent. | End-to-end check of the real write path; light load generation |

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `--mode` | *(required)* | `db` or `api` |
| `--seed` | `42` | Random seed. The same `--seed` and `--end` always produce identical data |
| `--repos` | `3` | Number of repositories |
| `--tests-per-repo` | `150` | Test cases per repository |
| `--runs-per-repo` | `300` | CI runs per repository, re-runs included |
| `--days` | `60` | Spread runs evenly over this many days |
| `--end` | now | End of the generated time range, RFC 3339 (e.g. `2026-10-12T00:00:00Z`) |
| `--url` | `http://localhost:8080` | *api mode:* ingest base URL (in the cluster: the Ingress IP) |
| `--api-key` | `$INGEST_API_KEY` | *api mode:* ingest API key |
| `--concurrency` | `8` | *api mode:* parallel requests |
| `--truncate` | `false` | *db mode:* wipe ingest-db's tables before loading. **ingest-db only:** reset analyzer-db separately, or the analyzer keeps stale stats |

`--mode=db` connects using `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER` and `DB_PASSWORD`. These must point at **ingest-db**.

### Examples

```bash
# Show every flag with its description and default value.
go run ./cmd/seed --help

# Point the seeder at ingest's local database (Postgres in Docker on port 5432).
# These variables configure the DB connection used by --mode=db.
export DB_HOST=localhost DB_PORT=5432 DB_NAME=ingest DB_USER=ingest DB_PASSWORD=ingestdb

# Fast initial load: bulk-insert the default 135,000 results into ingest-db.
#   --mode=db   write directly to ingest-db (one transaction, Postgres COPY)
go run ./cmd/seed --mode=db

# Wipe ingest-db's tables, then reload from scratch.
#   --truncate  empty repositories/test_cases/test_runs/test_results first
go run ./cmd/seed --mode=db --truncate

# Send data through ingest's public API instead, like a real CI pipeline would.
#   --mode=api        POST each run to /ingest/v1/runs
#   --url             base URL of the running ingest service
#   --api-key         must match ingest's INGEST_API_KEY
#   --seed=7          a different seed, so these runs don't collide with seed 42
#   --runs-per-repo   fewer runs per repository, for a quicker smoke test
go run ./cmd/seed --mode=api --url=http://localhost:8080 --api-key=ingestkey --seed=7 --runs-per-repo=50

# Run the same command again. Every run comes back "200 duplicate" and
# nothing is stored twice, which shows ingest's idempotency end to end.
go run ./cmd/seed --mode=api --url=http://localhost:8080 --api-key=ingestkey --seed=7 --runs-per-repo=50
```
