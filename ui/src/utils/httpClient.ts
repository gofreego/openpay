import { HttpClient, type RequestConfig } from '@gofreego/tsutils'

// Empty in local development, so calls go to the Vite dev server's proxy.
const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? ''

/**
 * DEV_OPERATOR is true when the console stands in for opengate (see
 * devOperatorHeaders). Always false in a production build.
 */
export const DEV_OPERATOR = import.meta.env.DEV && !!import.meta.env.VITE_DEV_USER_ID

export const httpClient = new HttpClient({
  baseURL: API_BASE_URL,
  timeout: 30000,
  headers: devOperatorHeaders(),
})

/**
 * devOperatorHeaders stands in for opengate when the console calls a local
 * OpenPay directly: opengate would inject the operator's identity and
 * permissions after OpenAuth validated the session. Vite replaces
 * import.meta.env.DEV with false in a production build, so these headers are
 * never sent there — only opengate may set them.
 */
function devOperatorHeaders(): Record<string, string> {
  if (!DEV_OPERATOR) return {}
  return {
    'x-user-id': import.meta.env.VITE_DEV_USER_ID!,
    'x-user-perms': import.meta.env.VITE_DEV_USER_PERMS ?? '',
  }
}

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
