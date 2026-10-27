import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useRepos, useTests } from '../api/hooks'
import { ClassBadge, Empty, ErrorCard, Pagination, ScoreBar, Spinner } from '../components/ui'
import { classificationLabel, percent } from '../format'
import { useUrlFilters } from '../useUrlFilters'
import type { Classification } from '../api/types'

const PAGE_SIZE = 25

// F2 - Flaky test leaderboard: ranked by flakiness score, with filters,
// search and pagination.
//
// The filters live in the URL query string (?repo=…&classification=…&q=…),
// not in component state (see useUrlFilters), so a filtered view can be
// shared as a link and survives a page refresh.
export default function Leaderboard() {
  const { params, update, clear } = useUrlFilters()
  const repo = params.get('repo') ?? ''
  const classification = params.get('classification') ?? ''
  const q = params.get('q') ?? ''
  const offset = Number(params.get('offset') ?? 0) || 0

  // The search box keeps its own text so typing is instant; the URL (and so
  // the request) only updates 300 ms after the last keystroke.
  const [text, setText] = useState(q)
  const lastPushed = useRef(q) // the last value this page itself wrote to the URL

  // If the URL's search term changed from outside (Clear filters, or clicking
  // the Leaderboard link in the nav bar), show it in the box. A change we
  // pushed ourselves is ignored, so typing never gets overwritten mid-keystroke.
  useEffect(() => {
    if (q !== lastPushed.current) {
      lastPushed.current = q
      setText(q)
    }
  }, [q])

  // Debounce: push the typed text into the URL once typing pauses. Listing q
  // as a dependency means a pending timer is cancelled whenever the URL
  // changes under it (e.g. by Clear filters), so a stale timer can't fire.
  useEffect(() => {
    const value = text.trim()
    if (value === q) return
    const timer = setTimeout(() => {
      lastPushed.current = value
      update({ q: value })
    }, 300)
    return () => clearTimeout(timer)
  }, [text, q, update])

  const repos = useRepos()
  const tests = useTests({ repo, classification, q, limit: PAGE_SIZE, offset })

  return (
    <>
      <h1>Flaky test leaderboard</h1>
      <div className="filters" role="search">
        <label>
          Search
          <input type="search" placeholder="Test or file name…" value={text} onChange={(e) => setText(e.target.value)} />
        </label>
        <label>
          Repository
          <select value={repo} onChange={(e) => update({ repo: e.target.value })}>
            <option value="">All repositories</option>
            {repos.data?.map((r) => (
              <option key={r.name} value={r.name}>
                {r.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          Status
          <select value={classification} onChange={(e) => update({ classification: e.target.value })}>
            <option value="">All</option>
            {(Object.keys(classificationLabel) as Classification[]).map((c) => (
              <option key={c} value={c}>
                {classificationLabel[c]}
              </option>
            ))}
          </select>
        </label>
        {(repo || classification || q) && (
          <button
            className="btn"
            onClick={() => {
              lastPushed.current = ''
              setText('')
              clear()
            }}
          >
            Clear filters
          </button>
        )}
      </div>

      <section className="card flush">
        {tests.isPending ? (
          <Spinner />
        ) : tests.isError ? (
          <ErrorCard error={tests.error} onRetry={() => void tests.refetch()} />
        ) : tests.data.tests.length === 0 ? (
          <Empty>No tests match these filters.</Empty>
        ) : (
          <>
            <table>
              <thead>
                <tr>
                  <th>Test</th>
                  <th>Repository</th>
                  <th>Status</th>
                  <th>Flakiness score</th>
                  <th title="Passed / failed in the scoring window">Pass / fail</th>
                </tr>
              </thead>
              <tbody className={tests.isPlaceholderData ? 'stale' : undefined}>
                {tests.data.tests.map((t) => (
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
                    <td className="nowrap">
                      {t.pass_count} / {t.fail_count}
                      <span className="muted small"> ({percent(t.sample_size ? t.fail_count / t.sample_size : 0)} fail)</span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <Pagination total={tests.data.total} limit={PAGE_SIZE} offset={offset} onChange={(o) => update({ offset: o ? String(o) : '' })} />
          </>
        )}
      </section>
    </>
  )
}
