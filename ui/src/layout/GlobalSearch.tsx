import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { InputAdornment, TextField } from '@mui/material'
import SearchIcon from '@mui/icons-material/Search'
import { routeFor } from '../utils/routes'

/**
 * GlobalSearch jumps straight to anything by its id — pay_, ord_, wlt_,
 * jrn_ … — and treats anything else as a customer's OpenAuth id.
 */
export function GlobalSearch() {
  const navigate = useNavigate()
  const [value, setValue] = useState('')
  const go = () => {
    const v = value.trim()
    if (!v) return
    navigate(routeFor(v) ?? `/customers/${encodeURIComponent(v)}`)
    setValue('')
  }
  return (
    <TextField
      size="small"
      placeholder="Search an id or customer"
      value={value}
      onChange={(e) => setValue(e.target.value)}
      onKeyDown={(e) => { if (e.key === 'Enter') go() }}
      sx={{ minWidth: 260 }}
      InputProps={{ startAdornment: <InputAdornment position="start"><SearchIcon fontSize="small" /></InputAdornment> }}
    />
  )
}
