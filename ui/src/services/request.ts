import { httpClient, idempotent } from '../utils/httpClient'

export const BASE = '/openpay/v1'

type Query = Record<string, string | number | boolean | undefined | null>

/** query drops empty values, so an unset filter is not sent as "". */
export function query(params: Query = {}): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '' || v === false) continue
    q.append(k, String(v))
  }
  const s = q.toString()
  return s ? `?${s}` : ''
}

export async function get<T>(path: string, params?: Query): Promise<T> {
  return (await httpClient.get<T>(`${BASE}${path}${query(params)}`)).data
}

/** post sends a mutation; money-moving ones pass the action's idempotency key. */
export async function post<T>(path: string, body: unknown = {}, idempotencyKey?: string): Promise<T> {
  const config = idempotencyKey ? idempotent(idempotencyKey) : {}
  return (await httpClient.post<T>(`${BASE}${path}`, body, config)).data
}

export async function patch<T>(path: string, body: unknown, idempotencyKey?: string): Promise<T> {
  const config = idempotencyKey ? idempotent(idempotencyKey) : {}
  return (await httpClient.patch<T>(`${BASE}${path}`, body, config)).data
}

export const enc = encodeURIComponent
