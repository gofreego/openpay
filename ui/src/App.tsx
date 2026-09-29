import {
  LoginCallbackPage, NotFoundPage, NotificationProvider, ProtectedRoute, SidebarLayout, ThemeProvider,
} from '@gofreego/tsutils'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import DashboardIcon from '@mui/icons-material/Dashboard'
import { DashboardPage } from './pages/dashboard'
import { Shell } from './layout/Shell'
import { ConsoleProvider } from './hooks'
import { authService, sessionManager } from './services'

// The console lives under /payments/ (vite.config.ts base). OpenAuth sends
// the operator back to the callback with a login token.
const BASE = '/payments'
const LOGIN_URL = import.meta.env.VITE_LOGIN_URL
const CALLBACK = '/login-callback'

// Development only, and only when asked: run against a local OpenPay with no
// OpenAuth. Vite replaces import.meta.env.DEV with false in a production
// build, so this branch does not exist there.
const SKIP_LOGIN = import.meta.env.DEV && import.meta.env.VITE_DEV_SKIP_LOGIN === 'true'

// Only screens that exist appear; each later phase adds its own, gated on
// usePermissions() so operators are not shown what they cannot do.
const menuItems = [
  { id: 'dashboard', label: 'Dashboard', path: '/dashboard', icon: <DashboardIcon /> },
]

// Restores a stored session's Authorization header before anything renders,
// so the first request already carries it.
authService.initializeAuth()

function App() {
  const layout = <SidebarLayout menuItems={menuItems} isRouter={true} isBrowserRouter={false} style={{ height: '100vh' }} />

  return (
    <ThemeProvider>
      <NotificationProvider>
        <BrowserRouter basename={BASE}>
          <Routes>
            <Route path={CALLBACK} element={<LoginCallbackPage authService={authService} navigateTo="/dashboard" />} />
            <Route
              path="/"
              element={SKIP_LOGIN ? layout : (
                <ProtectedRoute sessionManager={sessionManager} loginUrl={LOGIN_URL} callbackPath={BASE + CALLBACK}>
                  {layout}
                </ProtectedRoute>
              )}
            >
              <Route element={<ConsoleProvider><Shell /></ConsoleProvider>}>
                <Route index element={<Navigate to="/dashboard" replace />} />
                <Route path="dashboard" element={<DashboardPage />} />
                <Route path="*" element={<NotFoundPage />} />
              </Route>
            </Route>
          </Routes>
        </BrowserRouter>
      </NotificationProvider>
    </ThemeProvider>
  )
}

export default App
