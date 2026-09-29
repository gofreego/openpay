import { Link as RouterLink, Outlet, useLocation } from 'react-router-dom'
import {
  Alert, Box, Breadcrumbs, Button, Chip, CircularProgress, Link, MenuItem, TextField, Tooltip, Typography,
} from '@mui/material'
import LockIcon from '@mui/icons-material/Lock'
import { useConsole } from '../hooks'
import { ApiErrorAlert } from '../components'
import { GlobalSearch } from './GlobalSearch'

/**
 * Shell is every page's frame inside the sidebar: breadcrumbs, the product
 * filter, and who is signed in. Pages render below it and read the filter
 * from useConsole().
 */
export function Shell() {
  const { me, loading, error, reload } = useConsole()

  if (loading) {
    return <Box sx={{ display: 'flex', justifyContent: 'center', p: 8 }}><CircularProgress /></Box>
  }
  if (error || !me) {
    return (
      <Box sx={{ p: 4, maxWidth: 640 }}>
        {error && <ApiErrorAlert error={error} title="OpenPay could not tell who you are" />}
        <Button sx={{ mt: 2 }} onClick={reload}>Try again</Button>
      </Box>
    )
  }
  if (!me.scopeAll && me.products.length === 0) {
    return (
      <Box sx={{ p: 4, maxWidth: 640 }}>
        <Alert severity="info">
          You are signed in, but your role grants no product in OpenPay. Ask an admin for an
          {' '}<code>openpay:scope:product:&lt;code&gt;</code> permission.
        </Alert>
      </Box>
    )
  }

  return (
    <Box sx={{ p: 3 }}>
      <Header />
      <Outlet />
    </Box>
  )
}

function Header() {
  const location = useLocation()
  const segments = location.pathname.split('/').filter(Boolean)

  return (
    <Box sx={{ display: 'flex', alignItems: 'center', gap: 2, mb: 3, flexWrap: 'wrap' }}>
      <Breadcrumbs sx={{ flex: 1 }}>
        {segments.map((segment, i) => {
          const label = segment.replace(/-/g, ' ')
          const to = '/' + segments.slice(0, i + 1).join('/')
          return i === segments.length - 1
            ? <Typography key={to} color="text.primary" sx={{ textTransform: 'capitalize' }}>{label}</Typography>
            : <Link key={to} component={RouterLink} to={to} underline="hover" color="inherit" sx={{ textTransform: 'capitalize' }}>{label}</Link>
        })}
      </Breadcrumbs>
      <GlobalSearch />
      <ProductFilter />
      <Operator />
    </Box>
  )
}

function ProductFilter() {
  const { permissions, products, selectedProduct, setSelectedProduct } = useConsole()
  const locked = !permissions.scopeAll && products.length === 1

  return (
    <TextField
      select
      size="small"
      label="Product"
      value={selectedProduct}
      onChange={(e) => setSelectedProduct(e.target.value)}
      disabled={locked}
      sx={{ minWidth: 200 }}
      InputProps={locked ? { endAdornment: <Tooltip title="Your role is scoped to this product"><LockIcon fontSize="small" sx={{ mr: 3 }} /></Tooltip> } : undefined}
    >
      {/* Only central ops may look across products (plan.md U-D6). */}
      {permissions.scopeAll && <MenuItem value="all">All products</MenuItem>}
      {products.map((p) => <MenuItem key={p.id} value={p.id}>{p.name} ({p.code})</MenuItem>)}
    </TextField>
  )
}

function Operator() {
  const { me, permissions } = useConsole()
  if (!me) return null
  return (
    <Tooltip title={<Box sx={{ whiteSpace: 'pre-line' }}>{me.permissions.join('\n') || 'No permissions'}</Box>}>
      <Chip
        label={`${me.userId} · ${permissions.scopeAll ? 'Central ops' : 'Product ops'}`}
        variant="outlined"
        size="small"
      />
    </Tooltip>
  )
}
