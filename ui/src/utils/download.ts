import { httpClient } from './httpClient'

/**
 * downloadFile fetches a server-generated file (a CSV statement) with the
 * session's headers and hands it to the browser to save. A plain link would
 * not carry the Authorization header opengate needs.
 */
export async function downloadFile(path: string, params: Record<string, string>, fallbackName: string): Promise<void> {
  const base = import.meta.env.VITE_API_BASE_URL ?? ''
  const url = `${base}${path}?${new URLSearchParams(params).toString()}`
  const response = await fetch(url, { headers: httpClient.getDefaultHeaders() })
  if (!response.ok) {
    const error = Object.assign(new Error(`HTTP Error: ${response.statusText}`), {
      status: response.status,
      data: await response.json().catch(() => undefined),
    })
    throw error
  }
  const disposition = response.headers.get('Content-Disposition') ?? ''
  const name = /filename="([^"]+)"/.exec(disposition)?.[1] ?? fallbackName

  const blob = await response.blob()
  const link = document.createElement('a')
  link.href = URL.createObjectURL(blob)
  link.download = name
  link.click()
  URL.revokeObjectURL(link.href)
}
