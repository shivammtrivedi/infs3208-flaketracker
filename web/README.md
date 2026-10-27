# FlakeTracker web dashboard

React + TypeScript single-page app (Vite). It only ever talks to the `api` service, through relative `/api/...` URLs. In development Vite proxies those to the api service; in the cloud the Ingress does.

## Pages

| Page | URL | What it does |
|---|---|---|
| Overview (F1) | `/` | Headline numbers, flaky/broken tests over the last 30 days, the five flakiest tests. Refreshes itself every 10 s |
| Leaderboard (F2) | `/tests` | All tests ranked by flakiness score; search, repository and status filters, pagination (filters live in the URL, so a filtered view is a shareable link) |
| Test detail (F3) | `/tests/:id` | Score breakdown, pass/fail timeline, duration trend, recent runs with CI links, and quarantine / release (F4) |
| Runs (F5) | `/runs` | CI runs with pass/fail bars; repository and branch filters |
| Run detail (F5) | `/runs/:id` | One run's results, failures and retries first |
| Quarantine (F4) | `/quarantine` | Every quarantined test, with a Release button |

The footer shows which `api` and `analyzer` builds are live and which scoring algorithm (`v1`/`v2`) is running. It re-checks every 10 s, so a rollout or rollback appears on screen without a refresh.

If a backend service is down, only the parts of a page that need it show an error; the rest of the dashboard keeps working (the overview shows a "reduced mode" banner).

## Code layout

```
src/
  api/types.ts        TypeScript types mirroring the Go API's JSON
  api/client.ts       fetch wrapper (ApiError carries the HTTP status)
  api/hooks.ts        one TanStack Query hook per endpoint, plus quarantine mutations
  useUrlFilters.ts    keeps filters in the URL query string
  format.ts           date / number / label helpers
  components/         Layout (header + footer) and shared UI (badges, error/loading states, pagination)
  pages/              one file per page above
```

## Commands

```bash
# Install the dependencies listed in package.json (run once, from this web/ folder).
npm install

# Start the dev server on http://localhost:5173 with hot reload.
# Requests to /api are proxied to the api service on http://localhost:8092.
npm run dev

# Same, but proxy /api to a different api address (the variable is read by vite.config.ts).
API_PROXY_TARGET=http://localhost:9000 npm run dev

# Type-check with tsc, then build the production bundle into dist/ (what the nginx image serves).
npm run build

# Lint the source with oxlint.
npm run lint
```
