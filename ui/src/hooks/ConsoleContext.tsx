import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import type { GetMeResponse } from '../apis/proto/openpay/v1/me'
import { meService } from '../services'
import { toApiError, type ApiError } from '../utils/apiError'
import { Permissions } from './permissions'
import { ConsoleContext, type ConsoleState, type ProductSelection } from './consoleState'


const STORAGE_KEY = 'openpay.console.product'

function remembered(): string | null {
  try {
    return window.localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

/**
 * ConsoleProvider loads the operator once (GET /me) and owns the product
 * filter. For central ops the filter is a convenience with an "All products"
 * default; for product ops it is a fixed constraint, locked to their
 * products with no "all" — the server would refuse anything wider anyway.
 */
export function ConsoleProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<GetMeResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<ApiError | null>(null)
  const [selection, setSelection] = useState<ProductSelection | null>(remembered())
  // Bumped by reload; each value is one fetch of /me.
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    let current = true
    meService.get()
      .then((response) => { if (current) { setMe(response); setError(null) } })
      .catch((err: unknown) => { if (current) setError(toApiError(err)) })
      .finally(() => { if (current) setLoading(false) })
    return () => { current = false }
  }, [attempt])

  const load = useCallback(() => {
    setLoading(true)
    setError(null)
    setAttempt((n) => n + 1)
  }, [])

  const value = useMemo<ConsoleState>(() => {
    const permissions = new Permissions(me)
    const products = me?.products ?? []
    const allowed = (s: ProductSelection | null): s is ProductSelection =>
      s !== null && (s === 'all' ? permissions.scopeAll : products.some((p) => p.id === s))
    const selectedProduct: ProductSelection = allowed(selection)
      ? selection
      : permissions.scopeAll
        ? 'all'
        : (products[0]?.id ?? 'all')

    return {
      me,
      loading,
      error,
      reload: load,
      permissions,
      products,
      selectedProduct,
      setSelectedProduct: (s) => {
        setSelection(s)
        try {
          window.localStorage.setItem(STORAGE_KEY, s)
        } catch {
          // A private window: the filter just is not remembered.
        }
      },
      productIds: selectedProduct === 'all' ? products.map((p) => p.id) : [selectedProduct],
    }
  }, [me, loading, error, selection, load])

  return <ConsoleContext.Provider value={value}>{children}</ConsoleContext.Provider>
}

