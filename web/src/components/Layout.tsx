import { NavLink, Outlet } from 'react-router-dom'
import { useVersion } from '../api/hooks'

const links = [
  { to: '/', label: 'Overview', end: true },
  { to: '/tests', label: 'Leaderboard' },
  { to: '/runs', label: 'Runs' },
  { to: '/quarantine', label: 'Quarantine' },
]

export default function Layout() {
  return (
    <>
      <header className="topbar">
        <div className="topbar-inner">
          <NavLink to="/" className="brand">
            <img src="/favicon.svg" alt="" width="26" height="26" />
            FlakeTracker
          </NavLink>
          <nav aria-label="Main">
            {links.map((l) => (
              <NavLink key={l.to} to={l.to} end={l.end} className={({ isActive }) => (isActive ? 'nav-link active' : 'nav-link')}>
                {l.label}
              </NavLink>
            ))}
          </nav>
        </div>
      </header>
      <main className="page">
        {/* <Outlet /> is where React Router renders whichever page matches the URL. */}
        <Outlet />
      </main>
      <Footer />
    </>
  )
}

// The footer proves which build is live. During the rollout demo the
// analyzer's algorithm flips v1 -> v2 (and back on rollback) right here.
function Footer() {
  const { data, isError } = useVersion()
  return (
    <footer className="footer">
      <div className="footer-inner">
        {data ? (
          <>
            <span title="api service build">
              api <code>{data.api.version}</code> · <code>{data.api.commit.slice(0, 7)}</code>
            </span>
            {data.analyzer ? (
              <span title="analyzer service build">
                analyzer <code>{data.analyzer.commit.slice(0, 7)}</code> · scoring algorithm{' '}
                <strong className="algo">{data.analyzer.algorithm_version}</strong>
              </span>
            ) : (
              <span className="muted">analyzer unavailable</span>
            )}
          </>
        ) : (
          <span className="muted">{isError ? 'version unavailable' : 'checking version…'}</span>
        )}
      </div>
    </footer>
  )
}
