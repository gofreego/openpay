import { authService } from '.'

const LOGIN_URL = import.meta.env.VITE_LOGIN_URL

/**
 * logout ends the session (best-effort server logout) and sends the operator
 * to OpenAuth's login page.
 */
export async function logout(): Promise<void> {
  try {
    await authService.logout({})
  } catch (error) {
    // AuthService.logout clears the stored session even when the call fails.
    console.error('Logout request failed:', error)
  }
  window.location.href = LOGIN_URL || '/'
}
