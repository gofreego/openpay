// Where each kind of public id opens. Prefixes come from pkg/ids.
const ROUTES: [string, string][] = [
  ['pay_', '/payments/'], ['ord_', '/orders/'], ['wlt_', '/wallets/'], ['cus_', '/customers/'],
  ['jrn_', '/ledger/journals/'], ['acc_', '/ledger/accounts/'], ['prd_', '/products/'], ['wtp_', '/wallet-types/'],
  ['dsp_', '/disputes/'], ['pot_', '/withdrawals/'], ['ref_', '/refunds/'],
]

export function routeFor(id: string): string | null {
  const match = ROUTES.find(([prefix]) => id.startsWith(prefix))
  return match ? match[1] + encodeURIComponent(id) : null
}
