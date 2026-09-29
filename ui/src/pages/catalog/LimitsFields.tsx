import { Box, TextField } from '@mui/material'
import { FIELDS, type LimitsDraft } from './limits'

export function LimitsFields({ draft, onChange, withdrawable, currency }: {
  draft: LimitsDraft; onChange: (d: LimitsDraft) => void; withdrawable: boolean; currency: string
}) {
  return (
    <Box sx={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 2, mt: 2 }}>
      {FIELDS.filter(([, , w]) => withdrawable || !w).map(([key, label]) => (
        <TextField key={key} size="small" label={`${label} (${currency})`} value={draft[key]}
          placeholder="No limit" inputProps={{ inputMode: 'decimal' }}
          onChange={(e) => onChange({ ...draft, [key]: e.target.value })} />
      ))}
    </Box>
  )
}
