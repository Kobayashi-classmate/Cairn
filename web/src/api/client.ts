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
  /**
   * Do not offer re-authentication when the server answers
   * `auth.step_up_required`. Used by the re-authentication call itself.
   */
  skipStepUp?: boolean
}

/** Header every state-changing request carries (CSRF guard); its value is "1". */
export const CSRF_HEADER = 'X-Cairn-Request'

/** Error code of a 403 response asking the user to confirm their identity again. */
export const STEP_UP_REQUIRED = 'auth.step_up_required'

/** True when an error is a step-up request the user declined (or that could not be offered). */
export function isStepUpRequired(err: unknown): boolean {
  return err instanceof ApiError && err.code === STEP_UP_REQUIRED
}

type UnauthorizedHandler = (error: ApiError) => void

/**
 * Asks the user to re-authenticate. Resolves once the session is elevated;
 * rejects (with the given error) when the user cancels.
 */
type StepUpHandler = (error: ApiError) => Promise<void>

let unauthorizedHandler: UnauthorizedHandler | null = null
let stepUpHandler: StepUpHandler | null = null

/** Registers the callback invoked when an authenticated request returns 401. */
export function setUnauthorizedHandler(handler: UnauthorizedHandler | null): void {
  unauthorizedHandler = handler
}

/** Registers the re-authentication prompt used for `auth.step_up_required`. */
export function setStepUpHandler(handler: StepUpHandler | null): void {
  stepUpHandler = handler
}

// The server's clock, estimated from the Date header of API responses.
// Pages compare the server's timestamps (expiry times, deadlines) with it
// rather than with this browser's clock, which may be off by minutes.
/** How many recent estimates the offset is the median of. */
const CLOCK_SAMPLES = 5
/** Recent estimates of the server's clock minus the browser's, in milliseconds. */
const clockSamples: number[] = []

function noteServerDate(response: Response, sent: number): void {
  const date = Date.parse(response.headers.get('Date') ?? '')
  if (!Number.isFinite(date)) return
  const received = Date.now()
  // The header has whole seconds: the server's time was within the second
  // after it, at about the middle of the request.
  clockSamples.push(date + 500 - (sent + received) / 2)
  if (clockSamples.length > CLOCK_SAMPLES) clockSamples.shift()
}

/** The server's clock minus this browser's, in milliseconds (0 before the first response). */
export function serverClockOffset(): number {
  if (clockSamples.length === 0) return 0
  const sorted = [...clockSamples].sort((a, b) => a - b)
  return sorted[Math.floor(sorted.length / 2)] ?? 0
}

/** The server's time now, in milliseconds since the epoch (to about a second). */
export function serverNow(): number {
  return Date.now() + serverClockOffset()
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
    headers[CSRF_HEADER] = '1'
  }
  let payload: string | undefined
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }

  let response: Response
  const sent = Date.now()
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
  noteServerDate(response, sent)

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
  if (response.status === 403 && error.code === STEP_UP_REQUIRED && !options.skipStepUp) {
    if (stepUpHandler) {
      // Rejects when the user cancels, which fails the original call.
      await stepUpHandler(error)
      // Retry exactly once; a second step-up request is returned as an error.
      return request<T>(method, path, body, { ...options, skipStepUp: true })
    }
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

/**
 * Turns an error response body (already read as text) into an ApiError, the
 * way JSON requests do. For callers that cannot use fetch, such as uploads
 * with progress through XMLHttpRequest.
 */
export function apiErrorFromBody(status: number, body: string): ApiError {
  try {
    const data: unknown = JSON.parse(body)
    if (isObject(data) && isObject(data.error) && typeof data.error.code === 'string') {
      return new ApiError(status, data.error.code, sanitizeParams(data.error.params))
    }
  } catch {
    // Not JSON; handled below.
  }
  return status === 0 ? new ApiError(0, ClientErrorCode.network) : new ApiError(status, ClientErrorCode.http, { status })
}

/** Runs the global 401 handling for a request made outside this client. */
export function reportUnauthorized(error: ApiError): void {
  unauthorizedHandler?.(error)
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
