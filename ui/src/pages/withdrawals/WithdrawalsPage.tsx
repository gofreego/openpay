import { useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Alert, Box, Button, MenuItem, TextField } from '@mui/material'
import { useNotification } from '@gofreego/tsutils'
import { WithdrawalStatus, type Withdrawal } from '../../apis/proto/openpay/v1/withdrawal'
import { ApiErrorAlert, AuditPanel, ConfirmAction, DataTable, EntityLink, KeyValue, Money, PageHeader, StatusChip } from '../../components'
import { Perm, useAsync, useConsole, usePermissions } from '../../hooks'
import { withdrawalService } from '../../services'
import { dateTime, enumLabel } from '../../utils/format'

export function WithdrawalsPage() {
  const navigate = useNavigate()
  const [status, setStatus] = useState<WithdrawalStatus | ''>(WithdrawalStatus.WITHDRAWAL_STATUS_PENDING_APPROVAL)
  const withdrawals = useAsync(() => withdrawalService.list({ status: status || undefined, limit: 100 }), [status])
  return (
    <>
      <PageHeader title="Withdrawals" help="withdrawals" subtitle="Cash-outs to customers' banks. Above a wallet type's threshold, a second person approves before anything is sent." />
      <DataTable<Withdrawal>
        rows={withdrawals.data?.withdrawals ?? []}
        rowKey={(x) => x.id}
        loading={withdrawals.loading}
        error={withdrawals.error}
        emptyText={status === WithdrawalStatus.WITHDRAWAL_STATUS_PENDING_APPROVAL ? 'Nothing waiting for approval' : 'None'}
        onRowClick={(x) => navigate(`/withdrawals/${x.id}`)}
        filters={
          <TextField size="small" select label="Status" value={status} sx={{ minWidth: 200 }} onChange={(e) => setStatus(e.target.value as WithdrawalStatus | '')}>
            <MenuItem value="">Any</MenuItem>
            {Object.values(WithdrawalStatus).filter((s) => s !== WithdrawalStatus.WITHDRAWAL_STATUS_UNSPECIFIED && s !== WithdrawalStatus.UNRECOGNIZED)
              .map((s) => <MenuItem key={s} value={s}>{enumLabel(s, 'WITHDRAWAL_STATUS_')}</MenuItem>)}
          </TextField>
        }
        columns={[
          { key: 'amount', header: 'Amount', align: 'right', render: (x) => <Money amount={x.amount} currency={x.currency} /> },
          { key: 'wallet', header: 'Wallet', render: (x) => <EntityLink id={x.walletId} /> },
          { key: 'status', header: 'Status', render: (x) => <StatusChip status={x.status} /> },
          { key: 'by', header: 'Requested by', render: (x) => x.requestedBy },
          { key: 'when', header: 'Requested', render: (x) => dateTime(x.createdAt) },
        ]}
      />
    </>
  )
}

export function WithdrawalDetailPage() {
  const { id = '' } = useParams()
  const { me } = useConsole()
  const permissions = usePermissions()
  const { showNotification } = useNotification()
  const withdrawal = useAsync(() => withdrawalService.get(id), [id])
  const [deciding, setDeciding] = useState<'approve' | 'reject' | null>(null)
  const x = withdrawal.data?.withdrawal
  if (withdrawal.error) return <ApiErrorAlert error={withdrawal.error} />
  if (!x) return null

  const pending = x.status === WithdrawalStatus.WITHDRAWAL_STATUS_PENDING_APPROVAL
  // Maker-checker: the server refuses an approval by the requester; the
  // console says so up front rather than letting them try.
  const ownRequest = me?.userId === x.requestedBy
  const canDecide = pending && permissions.canPlatform(Perm.withdrawalsApprove)

  return (
    <>
      <PageHeader title={<>Withdrawal <StatusChip status={x.status} size="medium" /></>} subtitle={<code>{x.id}</code>}
        action={canDecide && !ownRequest && (
          <Box sx={{ display: 'flex', gap: 1 }}>
            <Button variant="contained" onClick={() => setDeciding('approve')}>Approve</Button>
            <Button variant="outlined" color="error" onClick={() => setDeciding('reject')}>Reject</Button>
          </Box>
        )} />
      {canDecide && ownRequest && <Alert severity="info" sx={{ mb: 2 }}>You requested this withdrawal, so someone else must approve it.</Alert>}
      <KeyValue items={[
        ['Amount', <Money amount={x.amount} currency={x.currency} />],
        ['Wallet', <EntityLink id={x.walletId} />],
        ['To', <code>{x.beneficiaryId}</code>],
        ['Needed approval', x.requiresApproval ? 'Yes — above the wallet type threshold' : 'No'],
        ['Requested by', x.requestedBy],
        ['Decided by', x.decidedBy ? `${x.decidedBy} at ${dateTime(x.decidedAt)}: ${x.decisionNote}` : '—'],
        ['Failure', x.failureReason],
        ['Paid', dateTime(x.paidAt)],
        ['Requested', dateTime(x.createdAt)],
      ]} />
      <AuditPanel resourceType="withdrawal" resourceId={x.id} />
      <ConfirmAction
        open={deciding !== null}
        title={deciding === 'approve' ? 'Approve and send' : 'Reject'}
        destructive={deciding === 'reject'}
        description={deciding === 'approve'
          ? 'The money is sent to the customer\'s bank at once. Check the destination and the name at the bank on the customer\'s page first.'
          : 'The money goes back into the wallet. The customer can request again.'}
        amount={deciding === 'approve' ? { minor: x.amount, currency: x.currency } : undefined}
        requireNote
        confirmLabel={deciding === 'approve' ? 'Approve' : 'Reject'}
        onClose={() => setDeciding(null)}
        onConfirm={async ({ note, idempotencyKey }) => {
          if (deciding === 'approve') await withdrawalService.approve(x.id, note, idempotencyKey)
          else await withdrawalService.reject(x.id, note, idempotencyKey)
          showNotification(deciding === 'approve' ? 'Approved and sent' : 'Rejected', 'success')
          withdrawal.reload()
        }}
      />
    </>
  )
}
