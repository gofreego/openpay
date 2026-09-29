import { describe, expect, it } from 'vitest'
import { Permissions, Perm } from './permissions'
import { ProductStatus, type Product } from '../apis/proto/openpay/v1/product'

const product = (id: string, code: string): Product => ({
  id, code, name: code, status: ProductStatus.PRODUCT_STATUS_ACTIVE, defaultCurrency: 'INR',
  createdAt: undefined, updatedAt: undefined, refundDestination: 'source',
})

describe('Permissions', () => {
  it('lets central ops act on any product and on the platform', () => {
    const p = new Permissions({ userId: 'u', permissions: [Perm.paymentsRead, Perm.reconRead], scopeAll: true,
      products: [product('prd_z', 'zshala'), product('prd_b', 'bappa')] })
    expect(p.can(Perm.paymentsRead, 'prd_anything')).toBe(true)
    expect(p.canPlatform(Perm.reconRead)).toBe(true)
    expect(p.can(Perm.walletsAdjust)).toBe(false)
  })

  it('confines product ops to their products and keeps them off platform screens', () => {
    const p = new Permissions({ userId: 'u', permissions: [Perm.paymentsRead, Perm.reconRead], scopeAll: false,
      products: [product('prd_z', 'zshala')] })
    expect(p.can(Perm.paymentsRead)).toBe(true)
    expect(p.can(Perm.paymentsRead, 'prd_z')).toBe(true)
    expect(p.can(Perm.paymentsRead, 'prd_b')).toBe(false)
    // Holding the verb is not enough for a platform-level action.
    expect(p.canPlatform(Perm.reconRead)).toBe(false)
  })

  it('grants nothing without products, or before /me has loaded', () => {
    expect(new Permissions({ userId: 'u', permissions: [Perm.paymentsRead], scopeAll: false, products: [] })
      .can(Perm.paymentsRead)).toBe(false)
    expect(new Permissions(null).can(Perm.paymentsRead)).toBe(false)
  })
})
