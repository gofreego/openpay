import { useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Alert, MenuItem, TextField } from '@mui/material'
import { useNotification } from '@gofreego/tsutils'
import { DisputeStatus, type Dispute } from '../../apis/proto/openpay/v1/payment'
import { ApiErrorAlert, AuditPanel, ConfirmAction, DataTable, EntityLink, KeyValue, Money, PageHeader, StatusChip } from '../../components'
import { Perm, useAsync, usePermissions } from '../../hooks'
import { disputeService } from '../../services'
import { dateTime, enumLabel } from '../../utils/format'
import { Button } from '@mui/material'

function dueSoon(d: Dispute): boolean {
  if (!d.evidenceDueBy || d.status !== DisputeStatus.DISPUTE_STATUS_OPEN) return false
  return new Date(d.evidenceDueBy).getTime() - Date.now() < 3 * 86_400_000
}

export function DisputesPage() {
  const navigate = useNavigate()
  const [status, setStatus] = useState<DisputeStatus | ''>(DisputeStatus.DISPUTE_STATUS_OPEN)
  const disputes = useAsync(() => disputeService.list({ status: status || undefined, limit: 100 }), [status])
  return (
    <>
      <PageHeader title="Disputes" help="disputes" subtitle="Chargebacks. Evidence must reach the network before its due date or the dispute is lost by default." />
      <DataTable<Dispute>
        rows={disputes.data?.disputes ?? []}
        rowKey={(d) => d.id}
        loading={disputes.loading}
        error={disputes.error}
        onRowClick={(d) => navigate(`/disputes/${d.id}`)}
        filters={
          <TextField size="small" select label="Status" value={status} sx={{ minWidth: 160 }} onChange={(e) => setStatus(e.target.value as DisputeStatus | '')}>
            <MenuItem value="">Any</MenuItem>
            {Object.values(DisputeStatus).filter((s) => s !== DisputeStatus.DISPUTE_STATUS_UNSPECIFIED && s !== DisputeStatus.UNRECOGNIZED)
              .map((s) => <MenuItem key={s} value={s}>{enumLabel(s, 'DISPUTE_STATUS_')}</MenuItem>)}
          </TextField>
        }
        columns={[
          { key: 'payment', header: 'Payment', render: (d) => <EntityLink id={d.paymentId} /> },
          { key: 'amount', header: 'Amount', align: 'right', render: (d) => <Money amount={d.amount} currency={d.currency} /> },
          { key: 'reason', header: 'Reason', render: (d) => d.reason },
          { key: 'status', header: 'Status', render: (d) => <StatusChip status={d.status} /> },
          { key: 'due', header: 'Evidence due', render: (d) => <span style={{ color: dueSoon(d) ? 'red' : undefined, fontWeight: dueSoon(d) ? 700 : undefined }}>{dateTime(d.evidenceDueBy)}</span> },
          { key: 'opened', header: 'Opened', render: (d) => dateTime(d.createdAt) },
        ]}
      />
    </>
  )
}

export function DisputeDetailPage() {
  const { id = '' } = useParams()
  const permissions = usePermissions()
  const { showNotification } = useNotification()
  const dispute = useAsync(() => disputeService.get(id), [id])
  const [open, setOpen] = useState(false)
  const [evidence, setEvidence] = useState('')
  const d = dispute.data?.dispute
  if (dispute.error) return <ApiErrorAlert error={dispute.error} />
  if (!d) return null
  const canSubmit = d.status === DisputeStatus.DISPUTE_STATUS_OPEN && permissions.canPlatform(Perm.disputesManage)

  return (
    <>
      <PageHeader title={<>Dispute <StatusChip status={d.status} size="medium" /></>} subtitle={<code>{d.id}</code>}
        action={canSubmit && <Button variant="contained" onClick={() => { setEvidence(''); setOpen(true) }}>Submit evidence</Button>} />
      {dueSoon(d) && <Alert severity="warning" sx={{ mb: 2 }}>Evidence is due {dateTime(d.evidenceDueBy)}.</Alert>}
      <KeyValue items={[
        ['Payment', <EntityLink id={d.paymentId} />],
        ['Amount', <Money amount={d.amount} currency={d.currency} />],
        ['Reason', d.reason],
        ['Held from the wallet', <Money amount={d.fromWallet} currency={d.currency} />],
        ['Held from unapplied money', <Money amount={d.fromUnapplied} currency={d.currency} />],
        ['Not recoverable (expense if lost)', <Money amount={d.fromExpense} currency={d.currency} />],
        ['Evidence due', dateTime(d.evidenceDueBy)],
        ['Evidence', d.evidence ? `${d.evidence} — by ${d.evidenceSubmittedBy} at ${dateTime(d.evidenceSubmittedAt)}` : '—'],
        ['Resolved', dateTime(d.resolvedAt)],
        ['Opened', dateTime(d.createdAt)],
      ]} />
      <AuditPanel resourceType="dispute" resourceId={d.id} />
      <ConfirmAction
        open={open}
        title="Submit evidence"
        description={<>
          This speaks for the company to the card network, and can be submitted once.
          <TextField fullWidth multiline minRows={4} sx={{ mt: 2 }} label="Evidence" value={evidence} onChange={(e) => setEvidence(e.target.value)}
            helperText="Delivery proof, usage logs, the customer's own confirmation…" />
        </>}
        requireNote={false}
        confirmLabel="Submit"
        onClose={() => setOpen(false)}
        onConfirm={async ({ idempotencyKey }) => {
          if (!evidence.trim()) throw new Error('Write the evidence first')
          await disputeService.submitEvidence(d.id, evidence.trim(), idempotencyKey)
          showNotification('Evidence submitted', 'success')
          dispute.reload()
        }}
      />
    </>
  )
}
