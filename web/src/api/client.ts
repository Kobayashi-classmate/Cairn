// SPDX-License-Identifier: AGPL-3.0-only

/** Base path of the Cairn HTTP API. */
export const API_BASE = '/api/v1'

/** Parameters attached to an error code, used to fill in the translated message. */
export type ErrorParams = Record<string, string | number | boolean>

/**
 * Error codes produced by this client rather than by the server. The server
 * never uses the `client.` prefix.
 */
export const ClientErrorCode = {
  /** The request did not reach the server (offline, DNS, TLS, connection reset). */
  network: 'client.network_error',
  /** The server answered with an error status but without a Cairn error body. */
  http: 'client.http_error',
  /** A successful response body could not be decoded. */
  badResponse: 'client.bad_response',
} as const

/** A failed API call, normalized from the server's `{"error":{"code","params"}}` body. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly params: ErrorParams

  constructor(status: number, code: string, params: ErrorParams = {}) {
    super(`${code} (HTTP ${status})`)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.params = params
  }
}

export function isApiError(err: unknown): err is ApiError {
  return err instanceof ApiError
}

/** True when a request was cancelled through its AbortSignal. */
export function isAbortError(err: unknown): boolean {
  return err instanceof DOMException && err.name === 'AbortError'
}

export interface RequestOptions {
  signal?: AbortSignal
  /**
   * Do not run the global 401 handler for this request. Used by calls where
   * 401 is an expected answer (login, session probe, logout).
   */
  skipAuthRedirect?: boolean
}

type UnauthorizedHandler = (error: ApiError) => void

let unauthorizedHandler: UnauthorizedHandler | null = null

/** Registers the callback invoked when an authenticated request returns 401. */
export function setUnauthorizedHandler(handler: UnauthorizedHandler | null): void {
  unauthorizedHandler = handler
}

type Method = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'

async function request<T>(
  method: Method,
  path: string,
  body: unknown,
  options: RequestOptions = {},
): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (method !== 'GET') {
    // CSRF guard: the server rejects state-changing requests without it.
    headers['X-Cairn-Request'] = '1'
  }
  let payload: string | undefined
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }

  let response: Response
  try {
    response = await fetch(API_BASE + path, {
      method,
      headers,
      body: payload,
      credentials: 'same-origin',
      cache: 'no-store',
      signal: options.signal,
    })
  } catch (err) {
    if (isAbortError(err)) throw err
    throw new ApiError(0, ClientErrorCode.network)
  }

  if (response.ok) {
    if (response.status === 204) return undefined as T
    try {
      return (await response.json()) as T
    } catch (err) {
      if (isAbortError(err)) throw err
      throw new ApiError(response.status, ClientErrorCode.badResponse)
    }
  }

  const error = await readError(response)
  if (response.status === 401 && !options.skipAuthRedirect) {
    unauthorizedHandler?.(error)
  }
  throw error
}

async function readError(response: Response): Promise<ApiError> {
  try {
    const data: unknown = await response.json()
    if (isObject(data) && isObject(data.error) && typeof data.error.code === 'string') {
      return new ApiError(response.status, data.error.code, sanitizeParams(data.error.params))
    }
  } catch {
    // Not JSON (for example an error page from a reverse proxy); handled below.
  }
  return new ApiError(response.status, ClientErrorCode.http, { status: response.status })
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/** Keeps only scalar parameters; anything else cannot be interpolated into a message. */
function sanitizeParams(raw: unknown): ErrorParams {
  const params: ErrorParams = {}
  if (!isObject(raw)) return params
  for (const [key, value] of Object.entries(raw)) {
    if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
      params[key] = value
    }
  }
  return params
}

export const api = {
  get: <T>(path: string, options?: RequestOptions) => request<T>('GET', path, undefined, options),
  post: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>('POST', path, body, options),
  put: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>('PUT', path, body, options),
  patch: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>('PATCH', path, body, options),
  delete: <T>(path: string, options?: RequestOptions) =>
    request<T>('DELETE', path, undefined, options),
}
