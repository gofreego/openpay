import { HttpClient, type RequestConfig } from '@gofreego/tsutils'

// Empty in local development, so calls go to the Vite dev server's proxy.
const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? ''

export const httpClient = new HttpClient({
  baseURL: API_BASE_URL,
  timeout: 30000,
})

/**
 * idempotent adds the Idempotency-Key header to a request (plan.md U-D5).
 * The key belongs to the action, not the attempt: create it when the person
 * starts the action (ConfirmAction does) and reuse it for every retry, so a
 * double-click or a retry after a timeout cannot move money twice.
 */
export function idempotent(key: string, config: RequestConfig = {}): RequestConfig {
  return { ...config, headers: { ...(config.headers as Record<string, string>), 'Idempotency-Key': key } }
}

export default httpClient
