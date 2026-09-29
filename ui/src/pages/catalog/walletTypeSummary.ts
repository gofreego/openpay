import type { WalletType } from '../../apis/proto/openpay/v1/wallet_type'
import { ExpiryPolicy, WalletScope } from '../../apis/proto/openpay/v1/wallet_type'

/**
 * capabilitySummary says in words what a wallet type lets customers do
 * (plan.md U1): nobody should have to infer behaviour from checkboxes.
 */
export function capabilitySummary(t: Pick<WalletType, 'capabilities' | 'scope' | 'expiryPolicy' | 'code'>): string {
  const c = t.capabilities
  if (!c) return ''
  const where = t.scope === WalletScope.WALLET_SCOPE_PLATFORM ? 'in any product' : 'in this product'
  const can: string[] = []
  const cannot: string[] = []
  ;(c.fundable ? can : cannot).push(c.fundable ? 'top it up with real money' : 'top it up')
  if (c.grantable) can.push('receive granted value (promotions, cashback)')
  can.push(`spend it ${where}`)
  ;(c.transferable ? can : cannot).push('send it to another customer')
  ;(c.withdrawable ? can : cannot).push('cash it out to a bank account')
  const parts = [`Customers can ${join(can)}`]
  if (cannot.length) parts.push(`they cannot ${join(cannot)}`)
  if (c.refundableToSource) parts.push('refunds can go back to the card')
  if (t.expiryPolicy === ExpiryPolicy.EXPIRY_POLICY_ROLLING) parts.push('the balance lapses after inactivity')
  if (t.expiryPolicy === ExpiryPolicy.EXPIRY_POLICY_FIXED) parts.push('the balance lapses on a fixed date')
  return parts.join('; ') + '.'
}

function join(items: string[]): string {
  if (items.length <= 1) return items.join('')
  return `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`
}
