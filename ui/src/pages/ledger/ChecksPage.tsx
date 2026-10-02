import { useState } from 'react'
import { Box, Button, Typography } from '@mui/material'
import type { LedgerCheckRun } from '../../apis/proto/openpay/v1/ledger'
import { ApiErrorAlert, DataTable, PageHeader, StatusChip } from '../../components'
import { Perm, useAsync, usePermissions } from '../../hooks'
import { ledgerService } from '../../services'
import { dateTime } from '../../utils/format'
import { toApiError, type ApiError } from '../../utils/apiError'

export function ChecksPage() {
  const permissions = usePermissions()
  const runs = useAsync(() => ledgerService.checkRuns(20), [])
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<ApiError | null>(null)
  const [open, setOpen] = useState<string | null>(null)

  const run = async () => {
    setRunning(true)
    setError(null)
    try {
      await ledgerService.runCheck()
      runs.reload()
    } catch (err) {
      setError(toApiError(err))
    } finally {
      setRunning(false)
    }
  }

  return (
    <>
      <PageHeader title="Ledger checks" help="ledger-checks" subtitle="The invariants: every journal balances, every balance equals its postings. They run daily and at startup."
        action={permissions.can(Perm.ledgerCheck) && <Button variant="contained" disabled={running} onClick={() => void run()}>{running ? 'Checking…' : 'Run now'}</Button>} />
      {error && <Box sx={{ mb: 2 }}><ApiErrorAlert error={error} /></Box>}
      <DataTable<LedgerCheckRun>
        rows={runs.data?.runs ?? []}
        rowKey={(r) => r.id}
        loading={runs.loading}
        error={runs.error}
        onRowClick={(r) => setOpen(open === r.id ? null : r.id)}
        columns={[
          { key: 'when', header: 'Started', render: (r) => dateTime(r.startedAt) },
          { key: 'status', header: 'Result', render: (r) => <StatusChip status={r.status} /> },
          { key: 'trigger', header: 'Trigger', render: (r) => `${r.trigger}${r.triggeredBy ? ` · ${r.triggeredBy}` : ''}` },
          { key: 'findings', header: 'Findings', render: (r) => (
            <Box>
              {r.findings.length}{r.truncated ? '+' : ''}{r.error && ` · ${r.error}`}
              {open === r.id && r.findings.map((f, i) => (
                <Typography key={i} variant="body2" sx={{ fontFamily: 'monospace' }}>{f.invariant} · {f.subject} · {f.detail}</Typography>
              ))}
            </Box>
          ) },
        ]}
      />
    </>
  )
}
