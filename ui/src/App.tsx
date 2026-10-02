import { LoginCallbackPage, NotFoundPage, NotificationProvider, ProtectedRoute, ThemeProvider } from '@gofreego/tsutils'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { ConsoleProvider } from './hooks'
import { ConsoleLayout } from './layout/ConsoleLayout'
import { Shell } from './layout/Shell'
import { authService, sessionManager } from './services'
import { DEV_OPERATOR } from './utils/httpClient'
import { DashboardPage } from './pages/dashboard'
import { ProductsPage } from './pages/catalog/ProductsPage'
import { ProductDetailPage } from './pages/catalog/ProductDetailPage'
import { WalletTypesPage } from './pages/catalog/WalletTypesPage'
import { WalletTypeDetailPage } from './pages/catalog/WalletTypeDetailPage'
import { CustomersPage } from './pages/customers/CustomersPage'
import { CustomerDetailPage } from './pages/customers/CustomerDetailPage'
import { WalletDetailPage } from './pages/wallets/WalletDetailPage'
import { AccountsPage } from './pages/ledger/AccountsPage'
import { AccountPage } from './pages/ledger/AccountPage'
import { JournalPage } from './pages/ledger/JournalPage'
import { TrialBalancePage } from './pages/ledger/TrialBalancePage'
import { ChecksPage } from './pages/ledger/ChecksPage'
import { PaymentsPage } from './pages/payments/PaymentsPage'
import { PaymentDetailPage } from './pages/payments/PaymentDetailPage'
import { RefundRedirect, RefundsPage } from './pages/payments/RefundsPage'
import { DisputesPage, DisputeDetailPage } from './pages/payments/DisputesPage'
import { OrdersPage } from './pages/orders/OrdersPage'
import { OrderDetailPage } from './pages/orders/OrderDetailPage'
import { ProvidersPage } from './pages/providers/ProvidersPage'
import { ReconPage } from './pages/recon/ReconPage'
import { WithdrawalsPage, WithdrawalDetailPage } from './pages/withdrawals/WithdrawalsPage'
import { ReportsPage } from './pages/reports/ReportsPage'
import { AuditLogPage } from './pages/audit/AuditLogPage'
import { SettingsPage } from './pages/settings/SettingsPage'

// The console lives under /payments/ (vite.config.ts base). OpenAuth sends
// the operator back to the callback with a login token.
const BASE = '/payments'
const LOGIN_URL = import.meta.env.VITE_LOGIN_URL
const CALLBACK = '/login-callback'

// Development only, and only when asked: run against a local OpenPay with no
// OpenAuth. Vite replaces import.meta.env.DEV with false in a production
// build, so this branch does not exist there.
const SKIP_LOGIN = import.meta.env.DEV && import.meta.env.VITE_DEV_SKIP_LOGIN === 'true'

// Restores a stored session's Authorization header before anything renders,
// so the first request already carries it. Not when standing in for opengate:
// opengate consumes the OpenAuth token, and OpenPay reached directly would
// take it for a service credential and reject it.
if (!DEV_OPERATOR) authService.initializeAuth()

function App() {
  // The operator (GET /me) is loaded above the sidebar, so navigation can be
  // shaped by their permissions.
  const layout = <ConsoleProvider><ConsoleLayout /></ConsoleProvider>

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
              <Route element={<Shell />}>
                <Route index element={<Navigate to="/dashboard" replace />} />
                <Route path="dashboard" element={<DashboardPage />} />
                <Route path="products" element={<ProductsPage />} />
                <Route path="products/:id" element={<ProductDetailPage />} />
                <Route path="wallet-types" element={<WalletTypesPage />} />
                <Route path="wallet-types/:id" element={<WalletTypeDetailPage />} />
                <Route path="customers" element={<CustomersPage />} />
                <Route path="customers/:id" element={<CustomerDetailPage />} />
                <Route path="wallets/:id" element={<WalletDetailPage />} />
                <Route path="ledger/accounts" element={<AccountsPage />} />
                <Route path="ledger/accounts/:id" element={<AccountPage />} />
                <Route path="ledger/journals/:id" element={<JournalPage />} />
                <Route path="ledger/trial-balance" element={<TrialBalancePage />} />
                <Route path="ledger/checks" element={<ChecksPage />} />
                <Route path="payments" element={<PaymentsPage />} />
                <Route path="payments/:id" element={<PaymentDetailPage />} />
                <Route path="refunds" element={<RefundsPage />} />
                <Route path="refunds/:id" element={<RefundRedirect />} />
                <Route path="disputes" element={<DisputesPage />} />
                <Route path="disputes/:id" element={<DisputeDetailPage />} />
                <Route path="orders" element={<OrdersPage />} />
                <Route path="orders/:id" element={<OrderDetailPage />} />
                <Route path="providers" element={<ProvidersPage />} />
                <Route path="recon" element={<ReconPage />} />
                <Route path="withdrawals" element={<WithdrawalsPage />} />
                <Route path="withdrawals/:id" element={<WithdrawalDetailPage />} />
                <Route path="reports" element={<ReportsPage />} />
                <Route path="audit" element={<AuditLogPage />} />
                <Route path="settings" element={<SettingsPage />} />
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
