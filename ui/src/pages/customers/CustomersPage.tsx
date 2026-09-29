import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Box, Button, Paper, TextField, Typography } from '@mui/material'
import SearchIcon from '@mui/icons-material/Search'
import { ApiErrorAlert, PageHeader } from '../../components'
import { customerService } from '../../services'
import { toApiError, type ApiError } from '../../utils/apiError'

/**
 * CustomersPage finds one person by their OpenAuth user id or OpenPay
 * customer id. There is no browse-all list on purpose: support starts from
 * the person who wrote in.
 */
export function CustomersPage() {
  const navigate = useNavigate()
  const [ref, setRef] = useState('')
  const [error, setError] = useState<ApiError | null>(null)
  const [searching, setSearching] = useState(false)

  const search = async () => {
    setSearching(true)
    setError(null)
    try {
      const res = await customerService.get(ref.trim())
      if (res.customer) navigate(`/customers/${res.customer.id}`)
    } catch (err) {
      setError(toApiError(err))
    } finally {
      setSearching(false)
    }
  }

  return (
    <>
      <PageHeader title="Customers" subtitle="One person across every product, identified by their OpenAuth account." />
      <Paper variant="outlined" sx={{ p: 3, maxWidth: 640 }}>
        <Typography sx={{ mb: 2 }}>Look up by OpenAuth user id or OpenPay customer id (cus_…).</Typography>
        <Box component="form" sx={{ display: 'flex', gap: 2 }} onSubmit={(e) => { e.preventDefault(); void search() }}>
          <TextField fullWidth size="small" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="openauth user id or cus_…" autoFocus />
          <Button type="submit" variant="contained" startIcon={<SearchIcon />} disabled={!ref.trim() || searching}>Find</Button>
        </Box>
        {error && <Box sx={{ mt: 2 }}><ApiErrorAlert error={error} /></Box>}
      </Paper>
    </>
  )
}
