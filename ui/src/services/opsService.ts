import type {
  GetFeeVarianceResponse, GetReconSummaryResponse, GetSettlementResponse, ListReconBreaksResponse,
  ListSettlementsResponse, BreakAction, ResolveReconBreakResponse, RunReconCycleResponse,
} from '../apis/proto/openpay/v1/recon'
import type {
  DecideWithdrawalResponse, GetWithdrawalResponse, ListBeneficiariesResponse, ListWithdrawalsResponse, WithdrawalStatus,
} from '../apis/proto/openpay/v1/withdrawal'
import type { GetProductPnLResponse, GetProviderStatsResponse } from '../apis/proto/openpay/v1/report'
import { enc, get, post } from './request'

export const reconService = {
  summary: () => get<GetReconSummaryResponse>('/recon/summary'),
  runCycle: () => post<RunReconCycleResponse>('/recon/cycles'),
  settlements: (p: { provider?: string; limit?: number }) => get<ListSettlementsResponse>('/settlements', p),
  settlement: (id: string) => get<GetSettlementResponse>(`/settlements/${enc(id)}`),
  breaks: (p: { status?: string; limit?: number }) => get<ListReconBreaksResponse>('/recon/breaks', p),
  resolve: (id: string, body: { action: BreakAction; reasonCode: string; note: string; paymentId?: string }, key: string) =>
    post<ResolveReconBreakResponse>(`/recon/breaks/${enc(id)}/resolve`, body, key),
  feeVariance: (from: string, to: string) => get<GetFeeVarianceResponse>('/recon/fee-variance', { from, to }),
}

export const withdrawalService = {
  list: (p: { status?: WithdrawalStatus; customerId?: string; limit?: number }) =>
    get<ListWithdrawalsResponse>('/withdrawals', { status: p.status, customer_id: p.customerId, limit: p.limit }),
  get: (id: string) => get<GetWithdrawalResponse>(`/withdrawals/${enc(id)}`),
  approve: (id: string, note: string, key: string) => post<DecideWithdrawalResponse>(`/withdrawals/${enc(id)}/approve`, { note }, key),
  reject: (id: string, note: string, key: string) => post<DecideWithdrawalResponse>(`/withdrawals/${enc(id)}/reject`, { note }, key),
  beneficiaries: (customerId: string) => get<ListBeneficiariesResponse>(`/customers/${enc(customerId)}/beneficiaries`),
}

export const reportService = {
  pnl: (from: string, to: string, productId?: string) =>
    get<GetProductPnLResponse>('/reports/pnl', { from, to, product_id: productId }),
  providerStats: (from: string, to: string, productId?: string) =>
    get<GetProviderStatsResponse>('/reports/providers', { from, to, product_id: productId }),
}
