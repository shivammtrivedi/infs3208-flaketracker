import { Link, Route, Routes } from 'react-router-dom'
import Layout from './components/Layout'
import Leaderboard from './pages/Leaderboard'
import Overview from './pages/Overview'
import Quarantine from './pages/Quarantine'
import RunDetail from './pages/RunDetail'
import Runs from './pages/Runs'
import TestDetail from './pages/TestDetail'

// The URL -> page map. Layout renders the header and footer and puts the
// matching page where its <Outlet /> is.
export default function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Overview />} />
        <Route path="tests" element={<Leaderboard />} />
        <Route path="tests/:id" element={<TestDetail />} />
        <Route path="runs" element={<Runs />} />
        <Route path="runs/:id" element={<RunDetail />} />
        <Route path="quarantine" element={<Quarantine />} />
        <Route
          path="*"
          element={
            <>
              <h1>Page not found</h1>
              <Link to="/">Back to the overview</Link>
            </>
          }
        />
      </Route>
    </Routes>
  )
}
