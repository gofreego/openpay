import { AuthService, SessionManager } from '@gofreego/tsutils'
import httpClient from '../utils/httpClient'

export const sessionManager = SessionManager.getInstance()
export const authService = AuthService.getInstance(httpClient)
export { meService } from './meService'
export { productService, walletTypeService, credentialService, customerService, auditService } from './catalogService'
export { ledgerService, walletService } from './ledgerService'
export { paymentService, refundService, disputeService, orderService, providerService } from './paymentService'
export { reconService, withdrawalService, reportService } from './opsService'
