import { Link } from 'react-router-dom'
import { useRepos, useRuns } from '../api/hooks'
import { CountsBar, Empty, ErrorCard, Pagination, Spinner } from '../components/ui'
import { formatDateTime, shortSha, timeAgo } from '../format'
import { useUrlFilters } from '../useUrlFilters'

const PAGE_SIZE = 25

// F5 - Run explorer: ingested CI runs, newest first, filterable by
// repository and branch. Filters are kept in the URL (see Leaderboard).
export default function Runs() {
  const { params, update } = useUrlFilters()
  const repo = params.get('repo') ?? ''
  const branch = params.get('branch') ?? ''
  const offset = Number(params.get('offset') ?? 0) || 0

  const repos = useRepos()
  const runs = useRuns({ repo, branch, limit: PAGE_SIZE, offset })

  // The branch dropdown offers the branches of the selected repository, or of
  // all repositories when none is selected.
  const branches = [...new Set((repos.data ?? []).filter((r) => !repo || r.name === repo).flatMap((r) => r.branches))].sort()

  return (
    <>
      <h1>CI runs</h1>
      <div className="filters">
        <label>
          Repository
          <select value={repo} onChange={(e) => update({ repo: e.target.value, branch: '' })}>
            <option value="">All repositories</option>
            {repos.data?.map((r) => (
              <option key={r.name} value={r.name}>
                {r.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          Branch
          <select value={branch} onChange={(e) => update({ branch: e.target.value })}>
            <option value="">All branches</option>
            {branches.map((b) => (
              <option key={b} value={b}>
                {b}
              </option>
            ))}
          </select>
        </label>
      </div>

      <section className="card flush">
        {runs.isPending ? (
          <Spinner />
        ) : runs.isError ? (
          <ErrorCard error={runs.error} onRetry={() => void runs.refetch()} />
        ) : runs.data.runs.length === 0 ? (
          <Empty>No runs match these filters.</Empty>
        ) : (
          <>
            <table>
              <thead>
                <tr>
                  <th>Run</th>
                  <th>Repository</th>
                  <th>Branch</th>
                  <th>Commit</th>
                  <th>Started</th>
                  <th>Results</th>
                </tr>
              </thead>
              <tbody className={runs.isPlaceholderData ? 'stale' : undefined}>
                {runs.data.runs.map((r) => (
                  <tr key={r.run_id}>
                    <td className="nowrap">
                      <Link to={`/runs/${r.run_id}`}>#{r.run_id}</Link>
                      {r.ci_run_attempt > 1 && <span className="badge badge-retry" title="A re-run of the same CI run on the same commit">attempt {r.ci_run_attempt}</span>}
                    </td>
                    <td>{r.repository}</td>
                    <td>{r.branch}</td>
                    <td><code>{shortSha(r.commit_sha)}</code></td>
                    <td className="nowrap" title={formatDateTime(r.started_at)}>{timeAgo(r.started_at)}</td>
                    <td>
                      <CountsBar passed={r.passed} failed={r.failed} skipped={r.skipped} total={r.total} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <Pagination total={runs.data.total} limit={PAGE_SIZE} offset={offset} onChange={(o) => update({ offset: o ? String(o) : '' })} />
          </>
        )}
      </section>
    </>
  )
}
