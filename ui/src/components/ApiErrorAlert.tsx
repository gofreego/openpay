import { Alert, AlertTitle, Box, IconButton, Tooltip } from '@mui/material'
import ContentCopyIcon from '@mui/icons-material/ContentCopy'
import type { ApiError } from '../utils/apiError'

/**
 * ApiErrorAlert shows a failure the way support needs it: the message for
 * the operator, and the stable code and request id on a copyable line to
 * quote in a ticket.
 */
export function ApiErrorAlert({ error, title }: { error: ApiError; title?: string }) {
  const detail = `${error.code}${error.requestId ? ` · ${error.requestId}` : ''}`
  return (
    <Alert severity="error">
      {title && <AlertTitle>{title}</AlertTitle>}
      {error.message}
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5, mt: 0.5, fontFamily: 'monospace', fontSize: 12, opacity: 0.8 }}>
        {detail}
        <Tooltip title="Copy">
          <IconButton size="small" aria-label="copy error details" onClick={() => void navigator.clipboard?.writeText(detail)}>
            <ContentCopyIcon fontSize="inherit" />
          </IconButton>
        </Tooltip>
      </Box>
    </Alert>
  )
}
