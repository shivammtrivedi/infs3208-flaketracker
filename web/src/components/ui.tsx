import type { UseQueryResult } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { ApiError } from '../api/client'
import type { Classification, ResultStatus } from '../api/types'
import { classificationLabel, percent } from '../format'

export function ClassBadge({ value }: { value: Classification }) {
  return <span className={`badge badge-${value}`}>{classificationLabel[value]}</span>
}

export function StatusBadge({ status, retried }: { status: ResultStatus; retried?: boolean }) {
  if (status === 'passed' && retried) return <span className="badge badge-retry">Passed on retry</span>
  return <span className={`badge badge-${status}`}>{status[0].toUpperCase() + status.slice(1)}</span>
}

// A horizontal bar for a 0–1 flakiness score, with the number next to it so
// the meaning never depends on colour alone.
export function ScoreBar({ score }: { score: number }) {
  return (
    <div className="score" title={`Flakiness score ${score.toFixed(3)}`}>
      <div className="score-track">
        <div className="score-fill" style={{ width: `${Math.max(2, Math.min(1, score) * 100)}%` }} />
      </div>
      <span className="score-num">{score.toFixed(2)}</span>
    </div>
  )
}

export function Spinner({ label = 'Loading…' }: { label?: string }) {
  return (
    <div className="state" role="status">
      <span className="spinner" aria-hidden="true" />
      {label}
    </div>
  )
}

// What the user sees when a request fails. A 503 means one of the backend
// services is down; the message says so and that the rest of the app is still
// usable, which is the failure isolation the architecture is built for.
export function ErrorCard({ error, onRetry }: { error: Error; onRetry?: () => void }) {
  const unavailable = error instanceof ApiError && (error.status === 503 || error.status === 0)
  return (
    <div className="card error-card" role="alert">
      <strong>{unavailable ? 'Part of the system is unavailable' : 'Something went wrong'}</strong>
      <p>{error.message}</p>
      {unavailable && <p className="muted">The rest of the dashboard keeps working. This will recover on its own once the service is back.</p>}
      {onRetry && (
        <button className="btn" onClick={onRetry}>
          Try again
        </button>
      )}
    </div>
  )
}

// Renders `children(data)` once the query has data, a spinner while loading,
// and an ErrorCard if it failed - so each page doesn't repeat that logic.
// <T,> is a generic: the component works for any query result type T, and
// `data` in the render function is typed accordingly.
export function QueryBoundary<T>({ query, children }: { query: UseQueryResult<T, Error>; children: (data: T) => ReactNode }) {
  if (query.isPending) return <Spinner />
  if (query.isError) return <ErrorCard error={query.error} onRetry={() => void query.refetch()} />
  return <>{children(query.data)}</>
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="state muted">{children}</div>
}

// Previous / Next with a "26–50 of 450" summary.
export function Pagination({ total, limit, offset, onChange }: { total: number; limit: number; offset: number; onChange: (offset: number) => void }) {
  if (total === 0) return null
  const from = offset + 1
  const to = Math.min(offset + limit, total)
  return (
    <div className="pagination">
      <span className="muted">
        {from}–{to} of {total}
      </span>
      <button className="btn" disabled={offset === 0} onClick={() => onChange(Math.max(0, offset - limit))}>
        ← Previous
      </button>
      <button className="btn" disabled={offset + limit >= total} onClick={() => onChange(offset + limit)}>
        Next →
      </button>
    </div>
  )
}

// A coloured bar showing a run's pass/fail split.
export function CountsBar({ passed, failed, skipped, total }: { passed: number; failed: number; skipped: number; total: number }) {
  if (total === 0) return <span className="muted">no results</span>
  return (
    <div className="counts" title={`${passed} passed, ${failed} failed, ${skipped} skipped (${percent(passed / total)} passed)`}>
      <div className="counts-bar" aria-hidden="true">
        <span className="seg seg-passed" style={{ width: `${(passed / total) * 100}%` }} />
        <span className="seg seg-failed" style={{ width: `${(failed / total) * 100}%` }} />
        <span className="seg seg-skipped" style={{ width: `${(skipped / total) * 100}%` }} />
      </div>
      <span className="counts-text">
        {passed} ✓ · {failed} ✗{skipped > 0 && ` · ${skipped} skipped`}
      </span>
    </div>
  )
}
