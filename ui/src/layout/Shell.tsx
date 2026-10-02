import { Outlet } from 'react-router-dom'
import { Alert, Box, Button, CircularProgress } from '@mui/material'
import { useConsole } from '../hooks'
import { ApiErrorAlert } from '../components'

/**
 * Shell is every page's frame inside the sidebar: it holds pages back until
 * the operator is known. The product filter and who is signed in live in the
 * sidebar (Sidebar.tsx); pages read the filter from useConsole().
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
      <Outlet />
    </Box>
  )
}
