import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useRun } from '../api/hooks'
import type { RunDetail as RunDetailData, RunResult } from '../api/types'
import { CountsBar, Empty, ErrorCard, QueryBoundary, StatusBadge } from '../components/ui'
import { formatDateTime, formatDuration, shortSha } from '../format'

type Show = 'problems' | 'all'

// F5 (drill-down) - one run and its test results. By default only the
// interesting ones are listed (failures and tests that needed a retry), since
// a run has ~150 results and nearly all of them simply passed.
export default function RunDetail() {
  const id = Number(useParams().id)
  const run = useRun(id)

  if (!Number.isInteger(id) || id < 1) return <ErrorCard error={new Error('That is not a valid run id.')} />

  return (
    <>
      <p>
        <Link to="/runs">← All runs</Link>
      </p>
      <QueryBoundary query={run}>{(r) => <Detail r={r} />}</QueryBoundary>
    </>
  )
}

function Detail({ r }: { r: RunDetailData }) {
  const [show, setShow] = useState<Show>('problems')
  const problems = r.results.filter((x) => x.final_status === 'failed' || x.passed_on_retry)
  const rows: RunResult[] = show === 'problems' ? problems : r.results

  return (
    <>
      <h1>Run #{r.run_id}</h1>
      <p className="muted">
        {r.repository} · {r.branch} · commit <code>{shortSha(r.commit_sha)}</code> · started {formatDateTime(r.started_at)}
        {r.ci_run_attempt > 1 && ` · attempt ${r.ci_run_attempt}`}
        {r.run_url && (
          <>
            {' · '}
            <a href={r.run_url} target="_blank" rel="noreferrer">
              open in CI ↗
            </a>
          </>
        )}
      </p>

      <section className="card">
        <CountsBar passed={r.passed} failed={r.failed} skipped={r.skipped} total={r.total} />
        <p className="muted small">
          {r.total} tests · {r.retried} passed only after a retry
        </p>
      </section>

      <div className="filters">
        <label>
          Show
          <select value={show} onChange={(e) => setShow(e.target.value as Show)}>
            <option value="problems">Failures and retries ({problems.length})</option>
            <option value="all">All results ({r.results.length})</option>
          </select>
        </label>
      </div>

      <section className="card flush">
        {rows.length === 0 ? (
          <Empty>No failures or retries in this run.</Empty>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Test</th>
                <th>Result</th>
                <th>Duration</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((x) => (
                <tr key={x.test_case_id}>
                  <td>
                    <Link to={`/tests/${x.test_case_id}`}>{x.name}</Link>
                    <div className="muted small">{x.suite}</div>
                    {x.error_message && <div className="error-text small">{x.error_message}</div>}
                  </td>
                  <td>
                    <StatusBadge status={x.final_status} retried={x.passed_on_retry} />
                    {x.attempts > 1 && <span className="muted small"> {x.attempts} attempts</span>}
                  </td>
                  <td className="nowrap">{formatDuration(x.duration_ms)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </>
  )
}
