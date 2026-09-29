import type { ReactElement } from 'react'
import { SidebarLayout } from '@gofreego/tsutils'
import DashboardIcon from '@mui/icons-material/Dashboard'
import StorefrontIcon from '@mui/icons-material/Storefront'
import CategoryIcon from '@mui/icons-material/Category'
import PeopleIcon from '@mui/icons-material/People'
import PaymentsIcon from '@mui/icons-material/Payments'
import ReceiptLongIcon from '@mui/icons-material/ReceiptLong'
import UndoIcon from '@mui/icons-material/Undo'
import GavelIcon from '@mui/icons-material/Gavel'
import AccountBalanceIcon from '@mui/icons-material/AccountBalance'
import BalanceIcon from '@mui/icons-material/Balance'
import FactCheckIcon from '@mui/icons-material/FactCheck'
import CompareArrowsIcon from '@mui/icons-material/CompareArrows'
import OutboxIcon from '@mui/icons-material/Outbox'
import RouterIcon from '@mui/icons-material/Router'
import AssessmentIcon from '@mui/icons-material/Assessment'
import HistoryIcon from '@mui/icons-material/History'
import { Perm, usePermissions, type Permissions } from '../hooks'

interface NavItem { id: string; label: string; path: string; icon: ReactElement; show: (p: Permissions) => boolean }

// Navigation shaped by permissions (plan.md U-D6): operators are not shown
// what they cannot use. Cosmetic — the server checks every call regardless.
const NAV: NavItem[] = [
  { id: 'dashboard', label: 'Dashboard', path: '/dashboard', icon: <DashboardIcon />, show: () => true },
  { id: 'customers', label: 'Customers', path: '/customers', icon: <PeopleIcon />, show: (p) => p.can(Perm.customersRead) || p.can(Perm.walletsRead) },
  { id: 'payments', label: 'Payments', path: '/payments', icon: <PaymentsIcon />, show: (p) => p.can(Perm.paymentsRead) },
  { id: 'orders', label: 'Orders', path: '/orders', icon: <ReceiptLongIcon />, show: (p) => p.can(Perm.paymentsRead) },
  { id: 'refunds', label: 'Refunds', path: '/refunds', icon: <UndoIcon />, show: (p) => p.can(Perm.paymentsRead) },
  { id: 'disputes', label: 'Disputes', path: '/disputes', icon: <GavelIcon />, show: (p) => p.can(Perm.paymentsRead) },
  { id: 'withdrawals', label: 'Withdrawals', path: '/withdrawals', icon: <OutboxIcon />, show: (p) => p.can(Perm.withdrawalsRead) },
  { id: 'accounts', label: 'Ledger accounts', path: '/ledger/accounts', icon: <AccountBalanceIcon />, show: (p) => p.can(Perm.ledgerRead) },
  { id: 'trial-balance', label: 'Trial balance', path: '/ledger/trial-balance', icon: <BalanceIcon />, show: (p) => p.can(Perm.ledgerRead) },
  { id: 'checks', label: 'Ledger checks', path: '/ledger/checks', icon: <FactCheckIcon />, show: (p) => p.can(Perm.ledgerRead) },
  { id: 'recon', label: 'Reconciliation', path: '/recon', icon: <CompareArrowsIcon />, show: (p) => p.canPlatform(Perm.reconRead) },
  { id: 'providers', label: 'Providers', path: '/providers', icon: <RouterIcon />, show: (p) => p.canPlatform(Perm.providersRead) },
  { id: 'reports', label: 'Reports', path: '/reports', icon: <AssessmentIcon />, show: (p) => p.can(Perm.ledgerRead) || p.can(Perm.paymentsRead) },
  { id: 'products', label: 'Products', path: '/products', icon: <StorefrontIcon />, show: (p) => p.can(Perm.productsRead) },
  { id: 'wallet-types', label: 'Wallet types', path: '/wallet-types', icon: <CategoryIcon />, show: (p) => p.can(Perm.walletTypesRead) },
  { id: 'audit', label: 'Audit log', path: '/audit', icon: <HistoryIcon />, show: (p) => p.can(Perm.auditRead) },
]

export function ConsoleLayout() {
  const permissions = usePermissions()
  const menuItems = NAV.filter((item) => item.show(permissions)).map(({ id, label, path, icon }) => ({ id, label, path, icon }))
  return <SidebarLayout menuItems={menuItems} isRouter={true} isBrowserRouter={false} style={{ height: '100vh' }} />
}
