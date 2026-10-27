import { useCallback, useEffect, useRef } from 'react'
import { useSearchParams } from 'react-router-dom'

// Filters live in the URL query string (?repo=…&q=…) rather than in component
// state, so a filtered view can be shared as a link and survives a refresh.
// Changes use `replace`, so each filter tweak or keystroke doesn't add a
// browser-history entry (the back button leaves the page instead of stepping
// through every filter state).
//
// `update` merges changes into the CURRENT URL. It reads the URL through a ref
// instead of a closure on purpose: the debounced search box calls update()
// 300 ms after typing, and a closure would hand it the URL as it was 300 ms
// ago - silently undoing a filter changed (or cleared) in the meantime.
export function useUrlFilters() {
  const [params, setParams] = useSearchParams()

  // useRef holds a value that persists across renders without causing one.
  // The effect (no dependency list = runs after every render) keeps it current.
  const latest = useRef({ params, setParams })
  useEffect(() => {
    latest.current = { params, setParams }
  })

  const update = useCallback((changes: Record<string, string>) => {
    const { params: current, setParams: set } = latest.current
    const next = new URLSearchParams(current)
    for (const [key, value] of Object.entries(changes)) {
      if (value) next.set(key, value)
      else next.delete(key)
    }
    // Changing any filter returns to the first page, unless the change is the page itself.
    if (!('offset' in changes)) next.delete('offset')
    set(next, { replace: true })
  }, [])

  const clear = useCallback(() => latest.current.setParams({}, { replace: true }), [])

  return { params, update, clear }
}
