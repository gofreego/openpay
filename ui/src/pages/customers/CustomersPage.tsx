import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Box, Button, Paper, TextField, Typography } from '@mui/material'
import SearchIcon from '@mui/icons-material/Search'
import { ApiErrorAlert, PageHeader } from '../../components'
import { customerService } from '../../services'
import { toApiError, type ApiError } from '../../utils/apiError'
import { routeFor } from '../../utils/routes'

/**
 * CustomersPage finds one person by their OpenAuth user id or OpenPay
 * customer id. There is no browse-all list on purpose: support starts from
 * the person who wrote in. Any other OpenPay id (pay_, ord_, wlt_ …) jumps
 * straight to that record, since a ticket often quotes one of those instead.
 */
export function CustomersPage() {
  const navigate = useNavigate()
  const [ref, setRef] = useState('')
  const [error, setError] = useState<ApiError | null>(null)
  const [searching, setSearching] = useState(false)

  const search = async () => {
    const v = ref.trim()
    const route = routeFor(v)
    if (route && !v.startsWith('cus_')) {
      navigate(route)
      return
    }
    setSearching(true)
    setError(null)
    try {
      const res = await customerService.get(v)
      if (res.customer) navigate(`/customers/${res.customer.id}`)
    } catch (err) {
      setError(toApiError(err))
    } finally {
      setSearching(false)
    }
  }

  return (
    <>
      <PageHeader title="Customers" help="customers" subtitle="One person across every product, identified by their OpenAuth account." />
      <Paper variant="outlined" sx={{ p: 3, maxWidth: 640 }}>
        <Typography sx={{ mb: 2 }}>Look up by OpenAuth user id or OpenPay customer id (cus_…), or paste any other OpenPay id (pay_, ord_, wlt_ …) to open it.</Typography>
        <Box component="form" sx={{ display: 'flex', gap: 2 }} onSubmit={(e) => { e.preventDefault(); void search() }}>
          <TextField fullWidth size="small" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="openauth user id, cus_… or any id" autoFocus />
          <Button type="submit" variant="contained" startIcon={<SearchIcon />} disabled={!ref.trim() || searching}>Find</Button>
        </Box>
        {error && <Box sx={{ mt: 2 }}><ApiErrorAlert error={error} /></Box>}
      </Paper>
    </>
  )
}
