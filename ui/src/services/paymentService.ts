import type {
  CreateRefundResponse, GetDisputeResponse, GetPaymentActivityResponse, GetPaymentResponse, GetRefundResponse,
  ListDisputesResponse, ListPaymentsResponse, ListRefundsResponse, PaymentStatus, RefundStatus, DisputeStatus,
  SearchRefundsResponse, SubmitDisputeEvidenceResponse, SyncPaymentResponse,
} from '../apis/proto/openpay/v1/payment'
import type {
  CancelOrderResponse, GetOrderResponse, ListOrdersResponse, OrderStatus, RefundOrderRequest, RefundOrderResponse,
} from '../apis/proto/openpay/v1/order'
import type { ListProvidersResponse, ProviderOverride, SetProviderOverrideResponse } from '../apis/proto/openpay/v1/provider'
import { enc, get, post } from './request'

export const paymentService = {
  list: (p: { limit?: number; offset?: number; productId?: string; customerId?: string; status?: PaymentStatus }) =>
    get<ListPaymentsResponse>('/payments', {
      limit: p.limit, offset: p.offset, product_id: p.productId, customer_id: p.customerId, status: p.status,
    }),
  get: (id: string) => get<GetPaymentResponse>(`/payments/${enc(id)}`),
  activity: (id: string) => get<GetPaymentActivityResponse>(`/payments/${enc(id)}/activity`),
  /** Asks the provider for the truth and applies it; safe to repeat. */
  sync: (id: string) => post<SyncPaymentResponse>(`/payments/${enc(id)}/sync`),
}

export const refundService = {
  forPayment: (paymentId: string) => get<ListRefundsResponse>(`/payments/${enc(paymentId)}/refunds`),
  search: (p: { status?: RefundStatus; productId?: string; limit?: number; offset?: number }) =>
    get<SearchRefundsResponse>('/refunds', { status: p.status, product_id: p.productId, limit: p.limit, offset: p.offset }),
  get: (id: string) => get<GetRefundResponse>(`/refunds/${enc(id)}`),
  create: (paymentId: string, body: { amount: string; reasonCode: string; memo: string }, key: string) =>
    post<CreateRefundResponse>(`/payments/${enc(paymentId)}/refunds`, body, key),
}

export const disputeService = {
  list: (p: { status?: DisputeStatus; limit?: number }) => get<ListDisputesResponse>('/disputes', p),
  get: (id: string) => get<GetDisputeResponse>(`/disputes/${enc(id)}`),
  submitEvidence: (id: string, evidence: string, key: string) =>
    post<SubmitDisputeEvidenceResponse>(`/disputes/${enc(id)}/evidence`, { evidence }, key),
}

export const orderService = {
  list: (p: { limit?: number; offset?: number; productId?: string; customerId?: string; status?: OrderStatus }) =>
    get<ListOrdersResponse>('/orders', {
      limit: p.limit, offset: p.offset, product_id: p.productId, customer_id: p.customerId, status: p.status,
    }),
  get: (id: string) => get<GetOrderResponse>(`/orders/${enc(id)}`),
  refund: (id: string, body: Omit<RefundOrderRequest, 'id'>, key: string) =>
    post<RefundOrderResponse>(`/orders/${enc(id)}/refunds`, body, key),
  cancel: (id: string, reason: string, key: string) => post<CancelOrderResponse>(`/orders/${enc(id)}/cancel`, { reason }, key),
}

export const providerService = {
  list: () => get<ListProvidersResponse>('/providers'),
  override: (name: string, override: ProviderOverride, reason: string, key: string) =>
    post<SetProviderOverrideResponse>(`/providers/${enc(name)}/override`, { override, reason }, key),
}
