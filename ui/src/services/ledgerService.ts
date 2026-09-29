import type {
  GetAccountStatementResponse, GetJournalResponse, GetLedgerAccountResponse, GetTrialBalanceResponse,
  LedgerAccountType, ListLedgerAccountsResponse, PostingDirection, ListLedgerCheckRunsResponse, RunLedgerCheckResponse,
} from '../apis/proto/openpay/v1/ledger'
import type {
  AdjustWalletResponse, GetFloatHeldResponse, GetWalletResponse, GetWalletStatementResponse, GrantWalletResponse,
  ListCustomerWalletsResponse,
} from '../apis/proto/openpay/v1/wallet'
import { downloadFile } from '../utils/download'
import { BASE, enc, get, post } from './request'

export const ledgerService = {
  listAccounts: (p: { limit?: number; offset?: number; productId?: string; platformOnly?: boolean; type?: LedgerAccountType; codePrefix?: string }) =>
    get<ListLedgerAccountsResponse>('/ledger/accounts', {
      limit: p.limit, offset: p.offset, product_id: p.productId, platform_only: p.platformOnly, type: p.type, code_prefix: p.codePrefix,
    }),
  getAccount: (idOrCode: string) => get<GetLedgerAccountResponse>(`/ledger/accounts/${enc(idOrCode)}`),
  statement: (idOrCode: string, cursor?: string, limit = 50) =>
    get<GetAccountStatementResponse>(`/ledger/accounts/${enc(idOrCode)}/statement`, { cursor, limit }),
  exportStatement: (idOrCode: string, from: string, to: string) =>
    downloadFile(`${BASE}/ledger/accounts/${enc(idOrCode)}/statement.csv`, { from, to }, 'statement.csv'),
  journal: (id: string) => get<GetJournalResponse>(`/ledger/journals/${enc(id)}`),
  trialBalance: (p: { productId?: string; asOf?: string; includeEmpty?: boolean }) =>
    get<GetTrialBalanceResponse>('/ledger/trial-balance', { product_id: p.productId, as_of: p.asOf, include_empty: p.includeEmpty }),
  checkRuns: (limit = 20) => get<ListLedgerCheckRunsResponse>('/ledger/checks', { limit }),
  runCheck: () => post<RunLedgerCheckResponse>('/ledger/checks'),
}

export const walletService = {
  get: (id: string) => get<GetWalletResponse>(`/wallets/${enc(id)}`),
  listForCustomer: (customerId: string) => get<ListCustomerWalletsResponse>(`/customers/${enc(customerId)}/wallets`),
  statement: (walletId: string, cursor?: string, limit = 50) =>
    get<GetWalletStatementResponse>(`/wallets/${enc(walletId)}/statement`, { cursor, limit }),
  exportStatement: (walletId: string, from: string, to: string) =>
    downloadFile(`${BASE}/wallets/${enc(walletId)}/statement.csv`, { from, to }, 'wallet-statement.csv'),
  grant: (walletId: string, body: { amount: string; reasonCode: string; memo: string; fundingProductId?: string }, key: string) =>
    post<GrantWalletResponse>(`/wallets/${enc(walletId)}/grants`, body, key),
  adjust: (walletId: string, body: { amount: string; direction: PostingDirection; reasonCode: string; memo: string; productId?: string }, key: string) =>
    post<AdjustWalletResponse>(`/wallets/${enc(walletId)}/adjustments`, body, key),
  floatHeld: () => get<GetFloatHeldResponse>('/float-held'),
}
