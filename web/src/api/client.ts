// A tiny fetch wrapper. All requests go to relative /api/... URLs: in
// development Vite proxies them to the api service, and in the cloud the
// Ingress routes /api/* to it, so the browser only ever sees one origin.

// ApiError carries the HTTP status so the UI can tell "service unavailable"
// (503) from "not found" (404). (Fields are declared explicitly rather than
// as constructor parameter properties, which this project's TypeScript
// settings - erasableSyntaxOnly - don't allow.)
export class ApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, init)
  } catch {
    // fetch() only rejects when the network itself fails (api unreachable).
    throw new ApiError(0, 'Cannot reach the API')
  }

  if (!res.ok) {
    // The Go services return errors as {"error": "..."}.
    let message = res.statusText
    try {
      const body = await res.json()
      if (body && typeof body.error === 'string') message = body.error
    } catch {
      // body wasn't JSON; keep the status text
    }
    throw new ApiError(res.status, message)
  }
  return res.json() as Promise<T>
}

export function get<T>(path: string, params?: Record<string, string | number | undefined>): Promise<T> {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(params ?? {})) {
    // Leave out unset and empty filters entirely.
    if (value !== undefined && value !== '') query.set(key, String(value))
  }
  const qs = query.toString()
  return request<T>(qs ? `${path}?${qs}` : path)
}

export function send<T>(method: 'POST' | 'DELETE', path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
}
