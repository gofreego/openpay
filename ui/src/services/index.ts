import { AuthService, SessionManager } from '@gofreego/tsutils'
import httpClient from '../utils/httpClient'

export const sessionManager = SessionManager.getInstance()
export const authService = AuthService.getInstance(httpClient)
export { meService } from './meService'
