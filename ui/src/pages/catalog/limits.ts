import type { WalletLimits } from '../../apis/proto/openpay/v1/wallet_type'
import { Money } from '../../utils/money'

export type LimitsDraft = Record<keyof WalletLimits, string>

export const FIELDS: [keyof WalletLimits, string, boolean][] = [
  ['maxBalance', 'Maximum balance', false],
  ['maxTxnAmount', 'Maximum per transaction', false],
  ['dailyLoadLimit', 'Daily load limit', false],
  ['minWithdrawalAmount', 'Minimum withdrawal', true],
  ['withdrawalApprovalThreshold', 'Withdrawals above this need approval', true],
  ['dailyWithdrawalLimit', 'Daily withdrawal limit', true],
]

/** limitsDraft shows a type's limits in rupees; 0 (no limit) as empty. */
export function limitsDraft(limits: WalletLimits | undefined, currency: string): LimitsDraft {
  const out = {} as LimitsDraft
  for (const [key] of FIELDS) {
    const v = limits?.[key] ?? '0'
    out[key] = v === '0' ? '' : Money.toMajor(v, currency)
  }
  return out
}

/** toLimits reads the rupee inputs back to minor units; empty means no limit. Throws on a bad amount. */
export function toLimits(draft: LimitsDraft, currency: string): WalletLimits {
  const out = {} as WalletLimits
  for (const [key, label] of FIELDS) {
    const v = draft[key].trim()
    if (v === '') { out[key] = '0'; continue }
    try {
      out[key] = Money.fromMajor(v, currency).toString()
    } catch (err) {
      throw new Error(`${label}: ${(err as Error).message}`)
    }
  }
  return out
}

