import { Alert, AlertTitle, Button } from '@mui/material'
import { Link as RouterLink } from 'react-router-dom'
import { LedgerCheckStatus } from '../apis/proto/openpay/v1/ledger'
import { Perm, useAsync, usePermissions } from '../hooks'
import { ledgerService } from '../services'
import { dateTime } from '../utils/format'

/**
 * LedgerCheckBanner makes drift impossible to miss (plan.md U2): if the
 * latest invariant check found anything, every ledger screen says so.
 */
export function LedgerCheckBanner() {
  const permissions = usePermissions()
  const runs = useAsync(() => ledgerService.checkRuns(1), [])
  const last = runs.data?.runs[0]
  if (!permissions.can(Perm.ledgerRead) || !last || last.status === LedgerCheckStatus.LEDGER_CHECK_STATUS_OK) return null
  const drift = last.status === LedgerCheckStatus.LEDGER_CHECK_STATUS_DRIFT
  return (
    <Alert severity="error" sx={{ mb: 2 }} action={<Button component={RouterLink} to="/ledger/checks" color="inherit" size="small">Details</Button>}>
      <AlertTitle>{drift ? `Ledger drift: ${last.findings.length}${last.truncated ? '+' : ''} invariant violations` : 'The last ledger check could not complete'}</AlertTitle>
      Found by the check at {dateTime(last.finishedAt ?? last.startedAt)}. This is an incident — see the ledger-drift runbook.
    </Alert>
  )
}
