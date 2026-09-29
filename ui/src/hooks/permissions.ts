import type { GetMeResponse } from '../apis/proto/openpay/v1/me'

/**
 * Permissions answers "may this operator do X, and on which products?"
 * (plan.md U-D6) from what the server resolved in GET /me.
 *
 * It shapes the console — which nav items and buttons appear — and is never
 * the security boundary: the server checks every call regardless.
 */
export class Permissions {
  private readonly held: Set<string>
  readonly scopeAll: boolean
  readonly productIds: string[]

  constructor(me: GetMeResponse | null) {
    this.held = new Set(me?.permissions ?? [])
    this.scopeAll = me?.scopeAll ?? false
    this.productIds = (me?.products ?? []).map((p) => p.id)
  }

  /**
   * can reports whether the operator holds permission — for one product when
   * productId is given, otherwise for at least one product in their scope.
   */
  can(permission: string, productId?: string): boolean {
    if (!this.held.has(permission)) return false
    if (this.scopeAll) return true
    if (productId === undefined) return this.productIds.length > 0
    return this.productIds.includes(productId)
  }

  /**
   * canPlatform is for platform-level actions (products, credentials, recon,
   * providers, adjustments), which need the whole-estate scope as well as
   * the verb.
   */
  canPlatform(permission: string): boolean {
    return this.scopeAll && this.held.has(permission)
  }
}

/** Permission names, as the server defines them (internal/auth). */
export const Perm = {
  productsRead: 'openpay:products:read',
  productsWrite: 'openpay:products:write',
  credentialsWrite: 'openpay:credentials:write',
  walletTypesRead: 'openpay:wallet_types:read',
  walletTypesWrite: 'openpay:wallet_types:write',
  walletTypesApproveWithdrawal: 'openpay:wallet_types:approve_withdrawal',
  customersRead: 'openpay:customers:read',
  paymentsRead: 'openpay:payments:read',
  paymentsSync: 'openpay:payments:sync',
  refundsCreate: 'openpay:refunds:create',
  disputesManage: 'openpay:disputes:manage',
  reconRead: 'openpay:recon:read',
  reconManage: 'openpay:recon:manage',
  withdrawalsRead: 'openpay:withdrawals:read',
  withdrawalsApprove: 'openpay:withdrawals:approve',
  providersRead: 'openpay:providers:read',
  providersManage: 'openpay:providers:manage',
  walletsRead: 'openpay:wallets:read',
  walletsGrant: 'openpay:wallets:grant',
  walletsAdjust: 'openpay:wallets:adjust',
  ledgerRead: 'openpay:ledger:read',
  ledgerCheck: 'openpay:ledger:check',
} as const
