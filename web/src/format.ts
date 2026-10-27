// Small display helpers shared by the pages.

import type { Classification } from './api/types'

export const classificationLabel: Record<Classification, string> = {
  stable: 'Stable',
  flaky: 'Flaky',
  broken: 'Broken',
  insufficient_data: 'Not enough data',
}

const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })
const dayMonth = new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short' })

export function formatDateTime(iso: string | null | undefined): string {
  return iso ? dateTime.format(new Date(iso)) : '-'
}

// "2026-10-12" (as sent for the daily chart) -> "12 Oct". The T12:00 avoids a
// time-zone shift moving the date to the previous/next day.
export function formatDay(day: string): string {
  return dayMonth.format(new Date(`${day}T12:00:00`))
}

export function timeAgo(iso: string | null | undefined): string {
  if (!iso) return 'never'
  const seconds = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000))
  if (seconds < 60) return 'just now'
  const units: [number, string][] = [[60, 'min'], [3600, 'hour'], [86400, 'day']]
  let value = seconds / 60
  let unit = 'min'
  for (const [size, name] of units) {
    if (seconds >= size) {
      value = seconds / size
      unit = name
    }
  }
  const n = Math.floor(value)
  return `${n} ${unit}${n === 1 ? '' : 's'} ago`
}

export const shortSha = (sha: string) => sha.slice(0, 7)

export const formatDuration = (ms: number) => (ms >= 1000 ? `${(ms / 1000).toFixed(1)} s` : `${ms} ms`)

export const percent = (fraction: number) => `${Math.round(fraction * 100)}%`
