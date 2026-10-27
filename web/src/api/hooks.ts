// One hook per api endpoint. TanStack Query (React Query) gives each hook
// caching, loading/error state and background refetching for free, so the
// pages don't hand-write useEffect + fetch + loading flags.
//
// A "query key" (the array first argument) identifies cached data: queries
// with the same key share one result, and invalidating a key makes every
// component using it refetch.
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { get, send } from './client'
import type {
  BuildInfo, Leaderboard, Quarantine, Repo, RunDetail, RunList, Summary, TestDetail, TestHistory,
} from './types'

// How often the overview and footer re-poll. Short on purpose: during the
// rollout demo the footer's version and the flaky count change on screen
// without anyone pressing refresh.
const LIVE_MS = 10_000

export interface TestFilters {
  repo?: string
  classification?: string
  q?: string
  limit: number
  offset: number
}

export interface RunFilters {
  repo?: string
  branch?: string
  limit: number
  offset: number
}

export const useSummary = () =>
  useQuery({ queryKey: ['summary'], queryFn: () => get<Summary>('/api/v1/summary'), refetchInterval: LIVE_MS })

export const useVersion = () =>
  useQuery({ queryKey: ['version'], queryFn: () => get<BuildInfo>('/api/v1/version'), refetchInterval: LIVE_MS })

export const useRepos = () =>
  useQuery({
    queryKey: ['repos'],
    queryFn: () => get<{ repos: Repo[] }>('/api/v1/repos').then((r) => r.repos),
    staleTime: 60_000,
  })

// placeholderData: keepPreviousData keeps showing the previous page while the
// next one loads, so typing in the search box or paging doesn't flash a spinner.
export const useTests = (f: TestFilters) =>
  useQuery({
    queryKey: ['tests', f],
    queryFn: () => get<Leaderboard>('/api/v1/tests', { ...f }),
    placeholderData: keepPreviousData,
  })

// `enabled: id > 0` skips the request for a nonsense id from the URL (NaN > 0
// is false), so /tests/abc shows its error without a pointless API call.
export const useTest = (id: number) =>
  useQuery({ queryKey: ['test', id], queryFn: () => get<TestDetail>(`/api/v1/tests/${id}`), enabled: id > 0 })

export const useHistory = (id: number) =>
  useQuery({
    queryKey: ['history', id],
    queryFn: () => get<TestHistory>(`/api/v1/tests/${id}/history`, { limit: 50 }),
    enabled: id > 0,
  })

export const useRuns = (f: RunFilters) =>
  useQuery({
    queryKey: ['runs', f],
    queryFn: () => get<RunList>('/api/v1/runs', { ...f }),
    placeholderData: keepPreviousData,
  })

export const useRun = (id: number) =>
  useQuery({ queryKey: ['run', id], queryFn: () => get<RunDetail>(`/api/v1/runs/${id}`), enabled: id > 0 })

export const useQuarantines = (repo?: string) =>
  useQuery({
    queryKey: ['quarantine', repo ?? ''],
    queryFn: () => get<{ quarantines: Quarantine[] }>('/api/v1/quarantine', { repo }).then((r) => r.quarantines),
  })

// After quarantining or releasing a test, everything that shows quarantine
// state is stale: the leaderboard flags, the test page, the quarantine list
// and the overview count. Invalidating by key prefix refetches them all.
function useInvalidateQuarantineViews() {
  const qc = useQueryClient()
  return () =>
    Promise.all(['tests', 'test', 'quarantine', 'summary'].map((key) => qc.invalidateQueries({ queryKey: [key] })))
}

export function useQuarantine() {
  const invalidate = useInvalidateQuarantineViews()
  return useMutation({
    mutationFn: (v: { test_case_id: number; reason: string }) => send<Quarantine>('POST', '/api/v1/quarantine', v),
    onSuccess: invalidate,
  })
}

export function useRelease() {
  const invalidate = useInvalidateQuarantineViews()
  return useMutation({
    mutationFn: (testCaseId: number) => send<Quarantine>('DELETE', `/api/v1/quarantine/${testCaseId}`),
    onSuccess: invalidate,
  })
}
