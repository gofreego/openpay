import { Box } from '@mui/material'
import { prettyJson } from '../utils/format'

/** JsonView shows raw JSON (a webhook, a provider call) read-only. */
export function JsonView({ raw, maxHeight = 320 }: { raw: string; maxHeight?: number }) {
  if (!raw) return null
  return (
    <Box component="pre" sx={{
      m: 0, p: 1.5, maxHeight, overflow: 'auto', fontSize: 12, borderRadius: 1, bgcolor: 'action.hover', whiteSpace: 'pre-wrap',
    }}>
      {prettyJson(raw)}
    </Box>
  )
}
