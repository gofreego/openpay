import type {
  CreateProductResponse, GetProductResponse, ListProductsResponse, ProductStatus, UpdateProductRequest, UpdateProductResponse,
} from '../apis/proto/openpay/v1/product'
import type {
  CreateWalletTypeRequest, CreateWalletTypeResponse, GetWalletTypeResponse, ListWalletTypesResponse,
  UpdateWalletTypeRequest, UpdateWalletTypeResponse,
} from '../apis/proto/openpay/v1/wallet_type'
import type {
  CreateServiceCredentialResponse, ListServiceCredentialsResponse, RevokeServiceCredentialResponse,
} from '../apis/proto/openpay/v1/credential'
import type { GetCustomerResponse } from '../apis/proto/openpay/v1/customer'
import type { ListAuditLogResponse } from '../apis/proto/openpay/v1/audit'
import { enc, get, patch, post } from './request'

export const productService = {
  list: (p: { limit?: number; offset?: number; search?: string; status?: ProductStatus }) =>
    get<ListProductsResponse>('/products', p),
  get: (id: string) => get<GetProductResponse>(`/products/${enc(id)}`),
  create: (body: { code: string; name: string; defaultCurrency: string }, key: string) =>
    post<CreateProductResponse>('/products', body, key),
  update: (id: string, body: Omit<UpdateProductRequest, 'id'>, key: string) =>
    patch<UpdateProductResponse>(`/products/${enc(id)}`, body, key),
}

export const walletTypeService = {
  list: (productId?: string) => get<ListWalletTypesResponse>('/wallet-types', { product_id: productId }),
  get: (id: string) => get<GetWalletTypeResponse>(`/wallet-types/${enc(id)}`),
  create: (body: CreateWalletTypeRequest, key: string) => post<CreateWalletTypeResponse>('/wallet-types', body, key),
  update: (id: string, body: Omit<UpdateWalletTypeRequest, 'id'>, key: string) =>
    patch<UpdateWalletTypeResponse>(`/wallet-types/${enc(id)}`, body, key),
}

export const credentialService = {
  list: (productId?: string) => get<ListServiceCredentialsResponse>('/credentials', { product_id: productId }),
  create: (productId: string, name: string, key: string) =>
    post<CreateServiceCredentialResponse>('/credentials', { productId, name }, key),
  revoke: (id: string, key: string) => post<RevokeServiceCredentialResponse>(`/credentials/${enc(id)}/revoke`, {}, key),
}

export const customerService = {
  /** By OpenPay customer id or OpenAuth external ref. */
  get: (idOrRef: string) => get<GetCustomerResponse>(`/customers/${enc(idOrRef)}`),
}

export const auditService = {
  list: (p: { resourceType?: string; resourceId?: string; actorId?: string; limit?: number; cursor?: string }) =>
    get<ListAuditLogResponse>('/audit', {
      resource_type: p.resourceType, resource_id: p.resourceId, actor_id: p.actorId, limit: p.limit, cursor: p.cursor,
    }),
}
