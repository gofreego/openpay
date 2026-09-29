import { createContext } from 'react'
import type { GetMeResponse } from '../apis/proto/openpay/v1/me'
import type { Product } from '../apis/proto/openpay/v1/product'
import type { ApiError } from '../utils/apiError'
import type { Permissions } from './permissions'

/** "all" is offered only to central ops (plan.md U-D6). */
export type ProductSelection = string | 'all'

export interface ConsoleState {
  me: GetMeResponse | null
  loading: boolean
  error: ApiError | null
  reload: () => void
  permissions: Permissions
  /** The products this operator may see. */
  products: Product[]
  /** The layout's product filter. */
  selectedProduct: ProductSelection
  setSelectedProduct: (selection: ProductSelection) => void
  /**
   * The product ids a screen should query for: every in-scope product under
   * "all", otherwise just the selected one.
   */
  productIds: string[]
}

export const ConsoleContext = createContext<ConsoleState | null>(null)
