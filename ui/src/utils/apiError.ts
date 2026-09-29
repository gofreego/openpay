import type { HttpError } from '@gofreego/tsutils'

/** The shape of every OpenPay error response (internal/middleware). */
export interface ApiError {
  /** Stable, machine-readable: "insufficient_balance", "not_found" … */
  code: string
  /** For people; may change, never parse it. */
  message: string
  /** Ties the failure to the server's logs. */
  requestId?: string
  /** HTTP status, when there was a response at all. */
  status?: number
}

/**
 * toApiError reads whatever a failed call threw into OpenPay's error shape,
 * so every screen can show the same message, code and request id.
 */
export function toApiError(error: unknown): ApiError {
  if (error instanceof Error) {
    const http = error as HttpError & { data?: { code?: unknown; message?: unknown; request_id?: unknown } }
    const body = http.data
    if (body && typeof body.code === 'string') {
      return {
        code: body.code,
        message: typeof body.message === 'string' ? body.message : error.message,
        requestId: typeof body.request_id === 'string' ? body.request_id : undefined,
        status: http.status,
      }
    }
    if (error.message === 'Request timeout') {
      // The server may still have done it. With an idempotency key a retry
      // is safe; that is why every money-moving action carries one.
      return { code: 'timeout', message: 'The request timed out. It may still have been applied — retrying is safe.' }
    }
    if (http.status) {
      return { code: `http_${http.status}`, message: error.message, status: http.status }
    }
    return { code: 'network', message: error.message }
  }
  return { code: 'unknown', message: 'An unexpected error occurred' }
}

/** One line for a toast: the message, then the code to quote in a ticket. */
export function describeError(error: unknown): string {
  const e = toApiError(error)
  return `${e.message} (${e.code}${e.requestId ? `, ${e.requestId}` : ''})`
}
