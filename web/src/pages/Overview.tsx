import { CartesianGrid, Legend, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { Link } from 'react-router-dom'
import { useSummary, useTests } from '../api/hooks'
import type { Summary } from '../api/types'
import { ClassBadge, Empty, ErrorCard, QueryBoundary, ScoreBar, Spinner } from '../components/ui'
import { formatDay, timeAgo } from '../format'

// F1 - Overview dashboard: headline numbers, the flaky-tests-over-time chart,
// and the current worst offenders.
export default function Overview() {
  const summary = useSummary()

  return (
    <>
      <h1>Overview</h1>
      <QueryBoundary query={summary}>{(s) => <SummaryView s={s} />}</QueryBoundary>
    </>
  )
}

function SummaryView({ s }: { s: Summary }) {
  const a = s.analyzer
  const dash = '-' // shown for a number whose service didn't answer

  return (
    <>
      {s.degraded.length > 0 && (
        <div className="banner" role="status">
          <strong>Running in reduced mode.</strong> {s.degraded.join(' and ')} {s.degraded.length === 1 ? 'is' : 'are'} not responding, so some numbers below are
          unavailable. Everything else keeps working and this page updates automatically when{' '}
          {s.degraded.length === 1 ? 'it recovers' : 'they recover'}.
        </div>
      )}

      <div className="stats">
        <Stat label="Tests tracked" value={a?.tests_tracked} fallback={dash} />
        <Stat label="Flaky" value={a?.classifications.flaky} tone="flaky" fallback={dash} />
        <Stat label="Broken" value={a?.classifications.broken} tone="broken" fallback={dash} />
        <Stat label="Quarantined" value={s.quarantined ?? undefined} fallback={dash} />
        <Stat label="Runs ingested" value={s.ingest?.runs_ingested} fallback={dash} />
      </div>

      <section className="card">
        <div className="card-head">
          <h2>Flaky and broken tests over time</h2>
          {a && (
            <span className="muted">
              Scoring algorithm <strong className="algo">{a.algorithm_version}</strong> · last analysed {timeAgo(a.last_cycle_at)}
            </span>
          )}
        </div>
        {a ? <DailyChart daily={a.daily} /> : <Empty>The analyzer is unavailable, so the history chart can't be drawn right now.</Empty>}
      </section>

      <section className="card">
        <div className="card-head">
          <h2>Flakiest tests right now</h2>
          <Link to="/tests">See the full leaderboard →</Link>
        </div>
        <TopTests />
      </section>
    </>
  )
}

function Stat({ label, value, tone, fallback }: { label: string; value: number | undefined; tone?: 'flaky' | 'broken'; fallback: string }) {
  return (
    <div className={`stat ${tone ? `stat-${tone}` : ''}`}>
      <div className="stat-value">{value === undefined ? fallback : value.toLocaleString()}</div>
      <div className="stat-label">{label}</div>
    </div>
  )
}

function DailyChart({ daily }: { daily: { day: string; flaky: number; broken: number }[] }) {
  if (daily.length === 0) return <Empty>No history yet - it appears after the first analysis cycle.</Empty>
  return (
    <div className="chart" role="img" aria-label="Line chart of flaky and broken test counts per day">
      <ResponsiveContainer width="100%" height={280}>
        <LineChart data={daily} margin={{ top: 8, right: 16, bottom: 0, left: 0 }}>
          <CartesianGrid strokeDasharray="3 3" stroke="#e3e8ef" />
          <XAxis dataKey="day" tickFormatter={formatDay} minTickGap={24} stroke="#5b6b82" />
          <YAxis allowDecimals={false} stroke="#5b6b82" />
          <Tooltip labelFormatter={(d) => formatDay(String(d))} />
          <Legend />
          {/* No draw-in animation: this chart re-renders every time the page
              polls for fresh data, and it should update quietly, not replay. */}
          <Line type="monotone" dataKey="flaky" name="Flaky" stroke="#d9830f" strokeWidth={2.5} dot={false} isAnimationActive={false} />
          <Line type="monotone" dataKey="broken" name="Broken" stroke="#c0392b" strokeWidth={2.5} dot={false} isAnimationActive={false} />
        </LineChart>
      </ResponsiveContainer>
    </div>
  )
}

function TopTests() {
  const top = useTests({ classification: 'flaky', limit: 5, offset: 0 })
  if (top.isPending) return <Spinner />
  if (top.isError) return <ErrorCard error={top.error} onRetry={() => void top.refetch()} />
  if (top.data.tests.length === 0) return <Empty>No flaky tests found.</Empty>
  return (
    // table-wrap lets a wide table scroll sideways inside the card on a narrow
    // screen instead of pushing the whole page wider.
    <div className="table-wrap">
    <table>
      <thead>
        <tr>
          <th>Test</th>
          <th>Repository</th>
          <th>Status</th>
          <th>Score</th>
        </tr>
      </thead>
      <tbody>
        {top.data.tests.map((t) => (
          <tr key={t.test_case_id}>
            <td>
              <Link to={`/tests/${t.test_case_id}`}>{t.name}</Link>
              <div className="muted small">{t.suite}</div>
            </td>
            <td>{t.repository}</td>
            <td>
              <ClassBadge value={t.classification} />
              {t.quarantined && <span className="badge badge-quarantined">Quarantined</span>}
            </td>
            <td>
              <ScoreBar score={t.flakiness_score} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
    </div>
  )
}
