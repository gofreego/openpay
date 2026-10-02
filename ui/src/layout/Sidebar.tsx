import { Box, Chip, MenuItem, TextField, Tooltip, Typography } from '@mui/material'
import HomeIcon from '@mui/icons-material/Home'
import LockIcon from '@mui/icons-material/Lock'
import { useConsole } from '../hooks'

/**
 * SidebarHeader tops the sidebar with the same home button and title as the
 * other admin apps (catalog). Home goes back to the admin landing page,
 * outside this console's /payments/ base.
 */
export function SidebarHeader() {
  return (
    <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', p: 2, position: 'relative' }}>
      <HomeIcon
        sx={{ cursor: 'pointer' }}
        onClick={() => window.location.href = '/'}
      />
      <Typography
        variant="h5"
        sx={{
          fontWeight: 'bold',
          color: 'primary.main',
          position: 'absolute',
          left: '50%',
          transform: 'translateX(-50%)',
          fontSize: '1.8rem',
        }}
      >
        OpenPay
      </Typography>
      <Box sx={{ width: 24 }} />
    </Box>
  )
}

/**
 * SidebarFooter holds the product filter every page reads and who is signed
 * in.
 */
export function SidebarFooter() {
  return (
    <Box sx={{ p: 2, display: 'flex', flexDirection: 'column', gap: 2 }}>
      <ProductFilter />
      <Operator />
    </Box>
  )
}

function ProductFilter() {
  const { me, permissions, products, selectedProduct, setSelectedProduct } = useConsole()
  // Until GET /me answers there is nothing to choose from; Shell shows the
  // loading state and any error.
  if (!me) return null
  const locked = !permissions.scopeAll && products.length === 1

  return (
    <TextField
      select
      fullWidth
      size="small"
      label="Product"
      value={selectedProduct}
      onChange={(e) => setSelectedProduct(e.target.value)}
      disabled={locked}
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
        sx={{ alignSelf: 'center', maxWidth: '100%' }}
      />
    </Tooltip>
  )
}
