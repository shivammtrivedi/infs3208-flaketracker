import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuarantines, useRelease, useRepos } from '../api/hooks'
import { Empty, QueryBoundary } from '../components/ui'
import { timeAgo } from '../format'

// F4 - Quarantine management: every test currently quarantined, with a way to
// release it. CI reads the same list from GET /api/v1/quarantine?repo=… to
// know which tests to skip.
export default function Quarantine() {
  const [repo, setRepo] = useState('')
  const repos = useRepos()
  const list = useQuarantines(repo || undefined)
  const release = useRelease()

  return (
    <>
      <h1>Quarantined tests</h1>
      <p className="muted">
        Quarantined tests are skipped by CI so they stop blocking merges while they're being fixed. Quarantine a test from its page on the{' '}
        <Link to="/tests">leaderboard</Link>.
      </p>
      <div className="filters">
        <label>
          Repository
          <select value={repo} onChange={(e) => setRepo(e.target.value)}>
            <option value="">All repositories</option>
            {repos.data?.map((r) => (
              <option key={r.name} value={r.name}>
                {r.name}
              </option>
            ))}
          </select>
        </label>
      </div>

      <section className="card flush">
        <QueryBoundary query={list}>
          {(items) =>
            items.length === 0 ? (
              <Empty>Nothing is quarantined{repo ? ' in this repository' : ''}.</Empty>
            ) : (
              <table>
                <thead>
                  <tr>
                    <th>Test</th>
                    <th>Repository</th>
                    <th>Reason</th>
                    <th>Quarantined</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {items.map((q) => (
                    <tr key={q.id}>
                      <td>
                        <Link to={`/tests/${q.test_case_id}`}>{q.name}</Link>
                        <div className="muted small">{q.suite}</div>
                      </td>
                      <td>{q.repository}</td>
                      <td>{q.reason}</td>
                      <td className="nowrap">
                        {timeAgo(q.created_at)}
                        <div className="muted small">by {q.created_by}</div>
                      </td>
                      <td>
                        <button
                          className="btn"
                          disabled={release.isPending && release.variables === q.test_case_id}
                          onClick={() => release.mutate(q.test_case_id)}
                        >
                          Release
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )
          }
        </QueryBoundary>
      </section>
      {release.isError && <div className="banner banner-error">{release.error.message}</div>}
    </>
  )
}
