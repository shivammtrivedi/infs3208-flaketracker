import { useState } from 'react'
import type { FormEvent } from 'react'
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { Link, useParams } from 'react-router-dom'
import { useHistory, useQuarantine, useRelease, useTest } from '../api/hooks'
import type { HistoryEntry, TestDetail as TestDetailData } from '../api/types'
import { ClassBadge, Empty, ErrorCard, QueryBoundary, Spinner, StatusBadge } from '../components/ui'
import { formatDateTime, formatDuration, percent, shortSha, timeAgo } from '../format'

// F3 - Test detail & history: score breakdown, a pass/fail timeline of recent
// runs, a duration trend, links to the CI runs - and (F4) the controls to
// quarantine or release the test.
export default function TestDetail() {
  const id = Number(useParams().id)
  const test = useTest(id)

  if (!Number.isInteger(id) || id < 1) return <ErrorCard error={new Error('That is not a valid test id.')} />

  return (
    <>
      <p>
        <Link to="/tests">← Leaderboard</Link>
      </p>
      <QueryBoundary query={test}>{(t) => <Detail t={t} />}</QueryBoundary>
    </>
  )
}

function Detail({ t }: { t: TestDetailData }) {
  const history = useHistory(t.test_case_id)

  return (
    <>
      <h1>{t.name}</h1>
      <p className="muted">
        {t.suite} · {t.repository}
      </p>

      <QuarantinePanel t={t} />

      <section className="card">
        <div className="card-head">
          <h2>Why it scores {t.flakiness_score.toFixed(2)}</h2>
          <ClassBadge value={t.classification} />
        </div>
        <div className="stats stats-compact">
          <Metric label="Flip rate" value={percent(t.flip_rate)} help="How often the result changes between consecutive runs" />
          <Metric label="Retry-pass rate" value={percent(t.retry_pass_rate)} help="Runs that failed, then passed when retried" />
          <Metric label="Same-commit conflicts" value={percent(t.commit_conflict_rate)} help="Commits where the test both passed and failed" />
          <Metric label="Pass / fail" value={`${t.pass_count} / ${t.fail_count}`} help={`Out of the last ${t.sample_size} results`} />
        </div>
        <p className="muted small">
          Scored by algorithm <strong className="algo">{t.algorithm_version}</strong> over the last {t.sample_size} of up to {t.window_size} results · updated{' '}
          {timeAgo(t.computed_at)}
        </p>
      </section>

      {history.isPending ? (
        <Spinner />
      ) : history.isError ? (
        <ErrorCard error={history.error} onRetry={() => void history.refetch()} />
      ) : history.data.results.length === 0 ? (
        <Empty>No results recorded for this test yet.</Empty>
      ) : (
        <History results={history.data.results} />
      )}
    </>
  )
}

function Metric({ label, value, help }: { label: string; value: string; help: string }) {
  return (
    <div className="stat" title={help}>
      <div className="stat-value">{value}</div>
      <div className="stat-label">{label}</div>
    </div>
  )
}

function History({ results }: { results: HistoryEntry[] }) {
  // The API returns newest first; timelines and charts read left to right
  // oldest to newest.
  const oldestFirst = [...results].reverse()

  return (
    <>
      <section className="card">
        <div className="card-head">
          <h2>Last {results.length} results</h2>
          <span className="muted small">oldest → newest</span>
        </div>
        <div className="timeline" role="list">
          {oldestFirst.map((r) => {
            const kind = r.final_status === 'passed' && r.passed_on_retry ? 'retry' : r.final_status
            return (
              <Link
                key={r.result_id}
                to={`/runs/${r.run_id}`}
                role="listitem"
                className={`tick tick-${kind}`}
                title={`${formatDateTime(r.started_at)} · ${r.branch} · ${r.final_status}${r.passed_on_retry ? ' (on retry)' : ''}${r.attempts > 1 ? `, ${r.attempts} attempts` : ''}`}
                aria-label={`${r.final_status} on ${formatDateTime(r.started_at)}`}
              >
                {kind === 'failed' ? '✗' : kind === 'retry' ? '↻' : kind === 'skipped' ? '–' : ''}
              </Link>
            )
          })}
        </div>
        <div className="legend small muted">
          <span><i className="tick tick-passed" /> passed</span>
          <span><i className="tick tick-retry" /> passed on retry</span>
          <span><i className="tick tick-failed" /> failed</span>
          <span><i className="tick tick-skipped" /> skipped</span>
        </div>
      </section>

      <section className="card">
        <div className="card-head">
          <h2>Duration trend</h2>
          <span className="muted small">failed runs shown in red - timeouts show up as spikes</span>
        </div>
        <DurationChart results={oldestFirst} />
      </section>

      <section className="card flush">
        <div className="card-head padded">
          <h2>Recent runs</h2>
        </div>
        <table>
          <thead>
            <tr>
              <th>When</th>
              <th>Branch</th>
              <th>Commit</th>
              <th>Result</th>
              <th>Duration</th>
              <th>CI run</th>
            </tr>
          </thead>
          <tbody>
            {results.slice(0, 15).map((r) => (
              <tr key={r.result_id}>
                <td className="nowrap" title={formatDateTime(r.started_at)}>{timeAgo(r.started_at)}</td>
                <td>{r.branch}</td>
                <td><code>{shortSha(r.commit_sha)}</code></td>
                <td>
                  <StatusBadge status={r.final_status} retried={r.passed_on_retry} />
                  {r.attempts > 1 && <span className="muted small"> {r.attempts} attempts</span>}
                  {r.error_message && <div className="error-text small">{r.error_message}</div>}
                </td>
                <td className="nowrap">{formatDuration(r.duration_ms)}</td>
                <td className="nowrap">
                  <Link to={`/runs/${r.run_id}`}>Run #{r.run_id}</Link>
                  {r.run_url && (
                    <>
                      {' · '}
                      <a href={r.run_url} target="_blank" rel="noreferrer">CI ↗</a>
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
    </>
  )
}

function DurationChart({ results }: { results: HistoryEntry[] }) {
  const data = results.map((r) => ({
    when: formatDateTime(r.started_at),
    seconds: Number((r.duration_ms / 1000).toFixed(2)),
    failed: r.final_status === 'failed',
  }))
  return (
    <div className="chart" role="img" aria-label="Line chart of test duration over recent runs">
      <ResponsiveContainer width="100%" height={240}>
        <LineChart data={data} margin={{ top: 8, right: 16, bottom: 0, left: 0 }}>
          <CartesianGrid strokeDasharray="3 3" stroke="#e3e8ef" />
          <XAxis dataKey="when" hide />
          <YAxis unit=" s" stroke="#5b6b82" />
          <Tooltip formatter={(v) => [`${v} s`, 'Duration']} />
          <Line
            type="monotone"
            dataKey="seconds"
            stroke="#2f5d9e"
            strokeWidth={2}
            isAnimationActive={false}
            // A custom dot lets failed runs be drawn bigger and red.
            dot={(p: { cx?: number; cy?: number; index?: number; payload?: { failed: boolean } }) => (
              <circle key={p.index} cx={p.cx} cy={p.cy} r={p.payload?.failed ? 5 : 2.5} fill={p.payload?.failed ? '#c0392b' : '#2f5d9e'} />
            )}
          />
        </LineChart>
      </ResponsiveContainer>
    </div>
  )
}

// F4 - quarantine controls. A quarantined test shows who/why and a Release
// button; otherwise a reason box and a Quarantine button.
function QuarantinePanel({ t }: { t: TestDetailData }) {
  const [reason, setReason] = useState('')
  const quarantine = useQuarantine()
  const release = useRelease()
  const q = t.quarantine

  function submit(e: FormEvent) {
    e.preventDefault()
    quarantine.mutate({ test_case_id: t.test_case_id, reason: reason.trim() }, { onSuccess: () => setReason('') })
  }

  if (q) {
    return (
      <div className="banner banner-quarantine">
        <div>
          <strong>Quarantined</strong> {timeAgo(q.created_at)} by {q.created_by}
          <div>Reason: {q.reason}</div>
          <div className="muted small">CI can fetch the quarantine list from the API and skip this test.</div>
        </div>
        <button className="btn" disabled={release.isPending} onClick={() => release.mutate(t.test_case_id)}>
          {release.isPending ? 'Releasing…' : 'Release quarantine'}
        </button>
        {release.isError && <div className="error-text">{release.error.message}</div>}
      </div>
    )
  }

  return (
    <form className="card quarantine-form" onSubmit={submit}>
      <label htmlFor="reason">Quarantine this test so it stops blocking pipelines while it's fixed</label>
      <div className="row">
        <input
          id="reason"
          type="text"
          placeholder="Reason (required) - e.g. times out under load, see ticket 123"
          value={reason}
          maxLength={500}
          onChange={(e) => setReason(e.target.value)}
        />
        <button className="btn btn-primary" type="submit" disabled={quarantine.isPending || reason.trim() === ''}>
          {quarantine.isPending ? 'Quarantining…' : 'Quarantine'}
        </button>
      </div>
      {quarantine.isError && <div className="error-text">{quarantine.error.message}</div>}
    </form>
  )
}
