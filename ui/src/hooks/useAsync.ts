import { useCallback, useEffect, useState } from 'react'
import { toApiError, type ApiError } from '../utils/apiError'

export interface Async<T> {
  data: T | null
  loading: boolean
  error: ApiError | null
  reload: () => void
}

/**
 * useAsync runs a read whenever its deps change, dropping stale answers so a
 * slow earlier request can never overwrite a newer one.
 */
export function useAsync<T>(load: () => Promise<T>, deps: unknown[]): Async<T> {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<ApiError | null>(null)
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    let current = true
    load()
      .then((result) => { if (current) { setData(result); setError(null) } })
      .catch((err: unknown) => { if (current) setError(toApiError(err)) })
      .finally(() => { if (current) setLoading(false) })
    return () => { current = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, attempt])

  const reload = useCallback(() => {
    setLoading(true)
    setAttempt((n) => n + 1)
  }, [])

  return { data, loading, error, reload }
}
