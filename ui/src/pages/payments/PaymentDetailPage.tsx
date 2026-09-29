import { useState, type ReactNode } from 'react'
import { useParams } from 'react-router-dom'
import {
  Alert, Box, Button, Chip, Paper, Table, TableBody, TableCell, TableRow, TextField, Typography,
} from '@mui/material'
import SyncIcon from '@mui/icons-material/Sync'
import { useNotification } from '@gofreego/tsutils'
import { PaymentApplication, PaymentStatus, type Payment, type Refund } from '../../apis/proto/openpay/v1/payment'
import {
  ApiErrorAlert, AuditPanel, ConfirmAction, DataTable, EntityLink, JsonView, KeyValue, Money, PageHeader, Section, StatusChip,
} from '../../components'
import { Perm, useAsync, usePermissions } from '../../hooks'
import { paymentService, refundService } from '../../services'
import { dateTime, enumLabel } from '../../utils/format'
import { toApiError, type ApiError } from '../../utils/apiError'
import { Money as M } from '../../utils/money'
import { Reasons } from '../../utils/reasons'

export function PaymentDetailPage() {
  const { id = '' } = useParams()
  const [version, setVersion] = useState(0)
  const payment = useAsync(() => paymentService.get(id), [id, version])
  const p = payment.data?.payment
  if (payment.error) return <ApiErrorAlert error={payment.error} />
  if (!p) return null
  const changed = () => setVersion((v) => v + 1)

  return (
    <>
      <PageHeader title={<>Payment <StatusChip status={p.status} size="medium" /></>} subtitle={<code>{p.id}</code>}
        action={<Box sx={{ display: 'flex', gap: 1 }}><SyncButton payment={p} onSynced={changed} /><RefundAction payment={p} onDone={changed} /></Box>} />
      <ApplicationNotice payment={p} />
      <KeyValue items={[
        ['For', p.purpose === 'PAYMENT_PURPOSE_WALLET_TOPUP' ? <>Top-up of <EntityLink id={p.walletId} /></> : 'Order'],
        ['Product', <EntityLink id={p.productId} />],
        ['Customer', <EntityLink id={p.customerId} />],
        ['Asked', <Money amount={p.amount} currency={p.currency} />],
        ['Captured', p.capturedAmount && p.capturedAmount !== '0' ? <Money amount={p.capturedAmount} currency={p.currency} /> : '—'],
        ['Refunded', <Money amount={p.refundedAmount || '0'} currency={p.currency} />],
        ['Where the money went', enumLabel(p.application, 'PAYMENT_APPLICATION_')],
        ['Provider', p.provider || '—'],
        ['Failure', p.failureCode ? `${p.failureCode}: ${p.failureReason}` : '—'],
        ['Description', p.description],
        ['Expires', dateTime(p.expiresAt)],
        ['Created', dateTime(p.createdAt)],
      ]} />
      <Attempts payment={p} />
      <Timeline payment={p} version={version} />
      <Refunds payment={p} version={version} />
      <AuditPanel resourceType="payment" resourceId={p.id} />
    </>
  )
}

function ApplicationNotice({ payment: p }: { payment: Payment }) {
  if (p.application === PaymentApplication.PAYMENT_APPLICATION_UNAPPLIED) {
    return <Alert severity="warning" sx={{ mb: 2 }}>Captured, but the wallet refused it (a limit). The money is owed back to the customer — refund it with reason <i>unapplied payment</i>.</Alert>
  }
  if (p.application === PaymentApplication.PAYMENT_APPLICATION_SUSPENSE) {
    return <Alert severity="warning" sx={{ mb: 2 }}>The provider's figures disagreed with ours; the money is parked in suspense for reconciliation.</Alert>
  }
  return null
}

/** SyncButton asks the provider for the truth and applies it — safe to repeat. */
function SyncButton({ payment, onSynced }: { payment: Payment; onSynced: () => void }) {
  const permissions = usePermissions()
  const { showNotification } = useNotification()
  const [syncing, setSyncing] = useState(false)
  const [error, setError] = useState<ApiError | null>(null)
  if (!permissions.can(Perm.paymentsSync, payment.productId)) return null
  return (
    <Box>
      <Button variant="outlined" startIcon={<SyncIcon />} disabled={syncing} onClick={async () => {
        setSyncing(true)
        setError(null)
        try {
          const res = await paymentService.sync(payment.id)
          showNotification(`Provider says: ${enumLabel(res.payment?.status ?? '', 'PAYMENT_STATUS_')}`, 'info')
          onSynced()
        } catch (err) {
          setError(toApiError(err))
        } finally {
          setSyncing(false)
        }
      }}>{syncing ? 'Asking provider…' : 'Sync from provider'}</Button>
      {error && <ApiErrorAlert error={error} />}
    </Box>
  )
}

function Attempts({ payment: p }: { payment: Payment }) {
  return (
    <Section title="Attempts">
      <DataTable
        rows={p.attempts}
        rowKey={(a) => a.id}
        emptyText="No attempt was made"
        columns={[
          { key: 'provider', header: 'Provider', render: (a) => a.provider },
          { key: 'why', header: 'Why this provider', render: (a) => a.routingReason },
          { key: 'ref', header: 'Provider id', render: (a) => <code>{a.providerPaymentId || '—'}</code> },
          { key: 'status', header: 'Status', render: (a) => <StatusChip status={a.status} /> },
          { key: 'failure', header: 'Failure', render: (a) => a.failureCode ? `${a.failureCode}: ${a.failureReason}` : '' },
          { key: 'at', header: 'Started', render: (a) => dateTime(a.createdAt) },
        ]}
      />
    </Section>
  )
}

interface TimelineItem { at: string; kind: 'state' | 'webhook' | 'call'; title: ReactNode; detail?: ReactNode; bad?: boolean }

/**
 * Timeline merges the payment's state transitions, the webhooks the provider
 * sent and the calls we made, in one chronological view (plan.md U3): which
 * provider, which error, which webhooks arrived — without reading a log.
 */
function Timeline({ payment: p, version }: { payment: Payment; version: number }) {
  const permissions = usePermissions()
  const activity = useAsync(() => paymentService.activity(p.id), [p.id, version])
  const [open, setOpen] = useState<number | null>(null)
  if (!permissions.can(Perm.paymentsRead, p.productId)) return null

  const items: TimelineItem[] = [
    ...p.transitions.map((t) => ({
      at: t.at ?? '', kind: 'state' as const,
      title: <>{enumLabel(t.from, 'PAYMENT_STATUS_')} → <b>{enumLabel(t.to, 'PAYMENT_STATUS_')}</b> <Typography component="span" color="text.secondary">via {t.source}</Typography></>,
      detail: t.detail || t.reference ? <Typography variant="body2">{t.detail} {t.reference && <code>{t.reference}</code>}</Typography> : undefined,
    })),
    ...(activity.data?.events ?? []).map((e) => ({
      at: e.receivedAt ?? '', kind: 'webhook' as const, bad: !!e.lastError,
      title: <>Webhook <b>{e.eventType}</b> from {e.provider}{e.processedAt ? '' : ' · not yet processed'}{e.lastError ? ` · failed ${e.attempts}×` : ''}</>,
      detail: <>{e.lastError && <Alert severity="error" sx={{ mb: 1 }}>{e.lastError}</Alert>}<JsonView raw={e.payload} /></>,
    })),
    ...(activity.data?.requests ?? []).map((r) => ({
      at: r.createdAt ?? '', kind: 'call' as const, bad: !!r.error,
      title: <>Called {r.provider} <b>{r.operation}</b> · {r.durationMs} ms{r.error ? ' · error' : ''}</>,
      detail: <>
        {r.error && <Alert severity="error" sx={{ mb: 1 }}>{r.error}</Alert>}
        {r.request && <><Typography variant="caption">Request</Typography><JsonView raw={r.request} /></>}
        {r.response && <><Typography variant="caption">Response</Typography><JsonView raw={r.response} /></>}
      </>,
    })),
  ].sort((a, b) => a.at.localeCompare(b.at))

  const colors = { state: 'primary', webhook: 'secondary', call: 'default' } as const
  return (
    <Section title="Timeline">
      {activity.error && <ApiErrorAlert error={activity.error} />}
      <Paper variant="outlined">
        <Table size="small">
          <TableBody>
            {items.map((item, i) => (
              <TableRow key={i} hover onClick={() => item.detail && setOpen(open === i ? null : i)} sx={{ cursor: item.detail ? 'pointer' : undefined }}>
                <TableCell sx={{ whiteSpace: 'nowrap', verticalAlign: 'top', width: 200 }}>{dateTime(item.at)}</TableCell>
                <TableCell sx={{ verticalAlign: 'top', width: 100 }}>
                  <Chip size="small" label={item.kind} color={item.bad ? 'error' : colors[item.kind]} variant="outlined" />
                </TableCell>
                <TableCell>
                  {item.title}
                  {open === i && <Box sx={{ mt: 1 }}>{item.detail}</Box>}
                </TableCell>
              </TableRow>
            ))}
            {items.length === 0 && <TableRow><TableCell>Nothing recorded yet.</TableCell></TableRow>}
          </TableBody>
        </Table>
      </Paper>
    </Section>
  )
}

function Refunds({ payment: p, version }: { payment: Payment; version: number }) {
  const refunds = useAsync(() => refundService.forPayment(p.id), [p.id, version])
  return (
    <Section title="Refunds">
      <DataTable<Refund>
        rows={refunds.data?.refunds ?? []}
        rowKey={(r) => r.id}
        loading={refunds.loading}
        error={refunds.error}
        emptyText="None"
        columns={[
          { key: 'id', header: 'Refund', render: (r) => <code>{r.id}</code> },
          { key: 'amount', header: 'Amount', align: 'right', render: (r) => <Money amount={r.amount} currency={r.currency} /> },
          { key: 'status', header: 'Status', render: (r) => <StatusChip status={r.status} /> },
          { key: 'to', header: 'Back to', render: (r) => r.source },
          { key: 'why', header: 'Reason', render: (r) => r.reasonCode },
          { key: 'who', header: 'By', render: (r) => r.requestedBy },
          { key: 'when', header: 'Requested', render: (r) => dateTime(r.createdAt) },
        ]}
      />
    </Section>
  )
}

/**
 * RefundAction returns money to the card. The form shows what is left to
 * refund and blocks going over it; the server refuses it regardless.
 */
function RefundAction({ payment: p, onDone }: { payment: Payment; onDone: () => void }) {
  const permissions = usePermissions()
  const { showNotification } = useNotification()
  const [open, setOpen] = useState(false)
  const [amount, setAmount] = useState('')
  const refundable = [PaymentStatus.PAYMENT_STATUS_CAPTURED, PaymentStatus.PAYMENT_STATUS_SETTLED,
    PaymentStatus.PAYMENT_STATUS_PARTIALLY_REFUNDED].includes(p.status)
  if (!refundable || !permissions.canPlatform(Perm.refundsCreate)) return null

  const remaining = M.sub(p.capturedAmount || '0', p.refundedAmount || '0')
  let minor: bigint | null = null
  let problem = ''
  try {
    if (amount) {
      minor = M.fromMajor(amount, p.currency)
      if (minor <= 0n) problem = 'Enter a positive amount'
      else if (minor > remaining) problem = `Only ${M.format(remaining, p.currency)} is left to refund`
    }
  } catch (err) {
    problem = (err as Error).message
  }
  const valid = minor !== null && !problem

  return (
    <>
      <Button variant="outlined" color="warning" onClick={() => { setAmount(M.toMajor(remaining, p.currency)); setOpen(true) }}>Refund</Button>
      <ConfirmAction
        open={open}
        title="Refund to the customer"
        destructive
        description={<>
          Left to refund: <b><Money amount={remaining} currency={p.currency} /></b> of <Money amount={p.capturedAmount} currency={p.currency} /> captured.
          {p.purpose === 'PAYMENT_PURPOSE_WALLET_TOPUP' && ' The wallet is debited first; a refund cannot take more than the wallet still holds.'}
          <TextField fullWidth sx={{ mt: 2 }} label={`Amount (${p.currency})`} value={amount} onChange={(e) => setAmount(e.target.value)}
            error={!!problem} helperText={problem || ' '} />
        </>}
        amount={valid ? { minor: minor!.toString(), currency: p.currency } : undefined}
        reasonCodes={[...Reasons.refund]}
        confirmLabel="Refund"
        onClose={() => setOpen(false)}
        onConfirm={async ({ reasonCode, note, idempotencyKey }) => {
          if (!valid) throw new Error(problem || 'Enter an amount')
          const res = await refundService.create(p.id, { amount: minor!.toString(), reasonCode, memo: note }, idempotencyKey)
          showNotification(`Refund ${res.refund?.id} ${enumLabel(res.refund?.status ?? '', 'REFUND_STATUS_').toLowerCase()}`, 'success')
          onDone()
        }}
      />
    </>
  )
}
