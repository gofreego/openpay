import { useState, type ReactNode } from 'react'
import {
  Box, Button, Dialog, DialogActions, DialogContent, DialogTitle, MenuItem, TextField, Typography,
} from '@mui/material'
import { Money } from '../utils/money'
import { newIdempotencyKey } from '../utils/idempotency'
import { toApiError, type ApiError } from '../utils/apiError'
import { ApiErrorAlert } from './ApiErrorAlert'

export interface Confirmation {
  reasonCode: string
  note: string
  /** Send as Idempotency-Key. The same for every retry of this action. */
  idempotencyKey: string
}

interface ConfirmActionProps {
  open: boolean
  title: string
  /** What will happen, in words. */
  description: ReactNode
  /** When money moves: the operator must retype it exactly to proceed. */
  amount?: { minor: string; currency: string }
  /** A fixed list: free-text reasons cannot be reported on. */
  reasonCodes?: string[]
  /** Whether a note is required. Default: required whenever there are reason codes. */
  requireNote?: boolean
  /** The journal (or other effect) this will produce, shown before submit. */
  preview?: ReactNode
  confirmLabel?: string
  destructive?: boolean
  onConfirm: (confirmation: Confirmation) => Promise<void>
  onClose: () => void
}

/**
 * ConfirmAction is the ceremony every money-moving or irreversible action
 * goes through (plan.md U-D7): retype the amount, pick a reason, explain it,
 * see the effect before submitting.
 *
 * It also owns the action's idempotency key (U-D5). The key is created when
 * the dialog opens and kept through failures and retries, so a double
 * submit, a timeout followed by a retry, or a flaky network can never move
 * the money twice. Only a new opening of the dialog is a new action.
 */
export function ConfirmAction(props: ConfirmActionProps) {
  // Remounting per opening gives each action a fresh key and empty fields.
  return props.open ? <ConfirmDialog {...props} /> : null
}

function ConfirmDialog({
  title, description, amount, reasonCodes, requireNote, preview, confirmLabel = 'Confirm',
  destructive = false, onConfirm, onClose,
}: ConfirmActionProps) {
  const [idempotencyKey] = useState(newIdempotencyKey)
  const [typedAmount, setTypedAmount] = useState('')
  const [reasonCode, setReasonCode] = useState('')
  const [note, setNote] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<ApiError | null>(null)

  const noteRequired = requireNote ?? (reasonCodes !== undefined && reasonCodes.length > 0)
  const amountMatches = (() => {
    if (!amount) return true
    try {
      return Money.fromMajor(typedAmount, amount.currency) === Money.parse(amount.minor)
    } catch {
      return false
    }
  })()
  const ready = amountMatches
    && (!reasonCodes?.length || reasonCode !== '')
    && (!noteRequired || note.trim() !== '')
    && !submitting

  const submit = async () => {
    setSubmitting(true)
    setError(null)
    try {
      await onConfirm({ reasonCode, note: note.trim(), idempotencyKey })
      onClose()
    } catch (err) {
      // Keep the dialog, the inputs and the key: retrying is the same action.
      setError(toApiError(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open onClose={submitting ? undefined : onClose} maxWidth="sm" fullWidth>
      <DialogTitle>{title}</DialogTitle>
      <DialogContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, pt: 1 }}>
          <Typography component="div">{description}</Typography>
          {preview && (
            <Box sx={{ p: 1.5, borderRadius: 1, bgcolor: 'action.hover' }}>
              <Typography variant="overline" color="text.secondary">What this will post</Typography>
              {preview}
            </Box>
          )}
          {amount && (
            <TextField
              label={`Type the amount to confirm: ${Money.format(amount.minor, amount.currency)}`}
              value={typedAmount}
              onChange={(e) => setTypedAmount(e.target.value)}
              error={typedAmount !== '' && !amountMatches}
              helperText={typedAmount !== '' && !amountMatches ? 'Does not match the amount' : ' '}
              inputProps={{ inputMode: 'decimal', autoComplete: 'off' }}
              autoFocus
            />
          )}
          {reasonCodes && reasonCodes.length > 0 && (
            <TextField select label="Reason" value={reasonCode} onChange={(e) => setReasonCode(e.target.value)} required>
              {reasonCodes.map((code) => <MenuItem key={code} value={code}>{code.replace(/_/g, ' ')}</MenuItem>)}
            </TextField>
          )}
          <TextField
            label={noteRequired ? 'Note (required)' : 'Note'}
            value={note}
            onChange={(e) => setNote(e.target.value)}
            multiline
            minRows={2}
            helperText="Recorded in the audit log with your name."
          />
          {error && <ApiErrorAlert error={error} title={error.code === 'timeout' ? 'Timed out' : 'Not done'} />}
        </Box>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={submitting}>Cancel</Button>
        <Button variant="contained" color={destructive ? 'error' : 'primary'} disabled={!ready} onClick={() => void submit()}>
          {submitting ? 'Working…' : error ? `Retry: ${confirmLabel}` : confirmLabel}
        </Button>
      </DialogActions>
    </Dialog>
  )
}
