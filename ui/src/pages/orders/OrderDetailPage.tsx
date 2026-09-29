import { useState } from 'react'
import { useParams } from 'react-router-dom'
import { Alert, Box, Button, Checkbox, FormControlLabel, MenuItem, TextField } from '@mui/material'
import { useNotification } from '@gofreego/tsutils'
import { OrderStatus, type Order, type OrderRefund, type OrderTender } from '../../apis/proto/openpay/v1/order'
import {
  ApiErrorAlert, AuditPanel, ConfirmAction, DataTable, EntityLink, KeyValue, Money, PageHeader, Section, StatusChip,
} from '../../components'
import { Perm, useAsync, usePermissions } from '../../hooks'
import { orderService } from '../../services'
import { dateTime } from '../../utils/format'
import { Money as M } from '../../utils/money'
import { Reasons } from '../../utils/reasons'

export function OrderDetailPage() {
  const { id = '' } = useParams()
  const order = useAsync(() => orderService.get(id), [id])
  const o = order.data?.order
  if (order.error) return <ApiErrorAlert error={order.error} />
  if (!o) return null

  const refunded = M.add(...o.refunds.map((r) => r.amount))
  // A hold still active on a finished order is customer money locked for nothing.
  const finished = o.status !== OrderStatus.ORDER_STATUS_PENDING_PAYMENT
  const stranded = finished && o.tenders.some((t) => t.status === 'held')

  return (
    <>
      <PageHeader title={<>Order <StatusChip status={o.status} size="medium" /></>} subtitle={<>{o.externalRef} · <code>{o.id}</code></>}
        action={<RefundOrder order={o} refunded={refunded} onDone={order.reload} />} />
      {stranded && <Alert severity="error" sx={{ mb: 2 }}>A wallet hold is still active on a finished order: customer money is locked. The hold sweeper should release it; if it persists, raise it.</Alert>}
      {!o.taxBreakdownProvided && <Alert severity="info" sx={{ mb: 2 }}>Booked without a tax breakdown: the whole amount was treated as revenue.</Alert>}
      <KeyValue items={[
        ['Product', <EntityLink id={o.productId} />],
        ['Customer', <EntityLink id={o.customerId} />],
        ['Invoice', o.invoiceRef],
        ['Subtotal', <Money amount={o.subtotal} currency={o.currency} />],
        ['Discount', <Money amount={o.discount} currency={o.currency} />],
        ['Tax', <>{<Money amount={o.tax} currency={o.currency} />}{o.taxRate && ` at ${o.taxRate}`}</>],
        ['Total', <b><Money amount={o.total} currency={o.currency} /></b>],
        ['Refunded', <Money amount={refunded} currency={o.currency} />],
        ['Card payment', o.paymentId ? <EntityLink id={o.paymentId} /> : 'None — paid from wallets'],
        ['Failure', o.failureReason],
        ['Expires', dateTime(o.expiresAt)],
        ['Paid', dateTime(o.paidAt)],
        ['Created', dateTime(o.createdAt)],
      ]} />
      <Section title="How it was paid">
        <DataTable<OrderTender>
          rows={o.tenders}
          rowKey={(t) => `${t.kind}:${t.walletId}`}
          columns={[
            { key: 'kind', header: 'Tender', render: (t) => t.kind === 'gateway' ? 'Card / UPI' : <>Wallet <EntityLink id={t.walletId} /></> },
            { key: 'amount', header: 'Amount', align: 'right', render: (t) => <Money amount={t.amount} currency={o.currency} /> },
            { key: 'status', header: 'Step', render: (t) => <StatusChip status={t.status === 'held' ? 'pending' : t.status === 'released' ? 'released' : t.status} /> },
          ]}
        />
      </Section>
      <Section title="Lines">
        <DataTable
          rows={o.lines}
          rowKey={(l) => `${l.itemId}:${l.description}`}
          columns={[
            { key: 'desc', header: 'Item', render: (l) => l.description || l.itemId },
            { key: 'qty', header: 'Qty', align: 'right', render: (l) => l.quantity },
            { key: 'unit', header: 'Unit', align: 'right', render: (l) => <Money amount={l.unitAmount} currency={o.currency} /> },
            { key: 'amount', header: 'Amount', align: 'right', render: (l) => <Money amount={l.amount} currency={o.currency} /> },
          ]}
        />
      </Section>
      <Section title="Refunds">
        <DataTable<OrderRefund>
          rows={o.refunds}
          rowKey={(r) => r.id}
          emptyText="None"
          columns={[
            { key: 'amount', header: 'Amount', align: 'right', render: (r) => <Money amount={r.amount} currency={o.currency} /> },
            { key: 'to', header: 'Went to', render: (r) => r.destination },
            { key: 'parts', header: 'Split', render: (r) => (
              <Box>{r.parts.map((p, i) => <Box key={i}><Money amount={p.amount} currency={o.currency} /> → {p.walletId ? <EntityLink id={p.walletId} /> : `card refund ${p.refundId}`}</Box>)}</Box>
            ) },
            { key: 'why', header: 'Reason', render: (r) => r.reasonCode },
            { key: 'when', header: 'When', render: (r) => dateTime(r.createdAt) },
          ]}
        />
      </Section>
      <AuditPanel resourceType="order" resourceId={o.id} />
    </>
  )
}

function RefundOrder({ order: o, refunded, onDone }: { order: Order; refunded: bigint; onDone: () => void }) {
  const permissions = usePermissions()
  const { showNotification } = useNotification()
  const [open, setOpen] = useState(false)
  const [amount, setAmount] = useState('')
  const [destination, setDestination] = useState('')
  const [split, setSplit] = useState(false)
  const [parts, setParts] = useState({ subtotal: '', discount: '', tax: '' })
  const refundable = [OrderStatus.ORDER_STATUS_PAID, OrderStatus.ORDER_STATUS_PARTIALLY_REFUNDED].includes(o.status)
  if (!refundable || !permissions.canPlatform(Perm.refundsCreate)) return null

  const remaining = M.sub(o.total, refunded)
  const read = (v: string) => { try { return v ? M.fromMajor(v, o.currency) : 0n } catch { return null } }
  const minor = read(amount)
  const sub = read(parts.subtotal), disc = read(parts.discount), tax = read(parts.tax)
  let problem = ''
  if (minor === null || minor <= 0n) problem = 'Enter a positive amount'
  else if (minor > remaining) problem = `Only ${M.format(remaining, o.currency)} is left to refund`
  else if (split && (sub === null || disc === null || tax === null)) problem = 'The split has an invalid amount'
  else if (split && sub! - disc! + tax! !== minor) problem = 'Subtotal - discount + tax must equal the amount'

  return (
    <>
      <Button variant="outlined" color="warning" onClick={() => { setAmount(M.toMajor(remaining, o.currency)); setOpen(true) }}>Refund</Button>
      <ConfirmAction
        open={open}
        title="Refund order"
        destructive
        description={<>
          Left to refund: <b><Money amount={remaining} currency={o.currency} /></b>. Each share goes back the way it came — wallet
          shares to their wallets, the card share to the card — unless you send it all to store credit.
          <TextField fullWidth sx={{ mt: 2 }} label={`Amount (${o.currency})`} value={amount} onChange={(e) => setAmount(e.target.value)} />
          <TextField fullWidth select sx={{ mt: 2 }} label="Send to" value={destination} onChange={(e) => setDestination(e.target.value)}>
            <MenuItem value="">The product's default</MenuItem>
            <MenuItem value="source">Back the way it was paid</MenuItem>
            <MenuItem value="wallet">Store credit (wallet)</MenuItem>
          </TextField>
          <FormControlLabel sx={{ mt: 1 }} control={<Checkbox checked={split} onChange={(_, v) => setSplit(v)} />} label="Give the tax split myself (otherwise proportional)" />
          {split && (
            <Box sx={{ display: 'flex', gap: 1 }}>
              <TextField size="small" label="Subtotal" value={parts.subtotal} onChange={(e) => setParts({ ...parts, subtotal: e.target.value })} />
              <TextField size="small" label="Discount" value={parts.discount} onChange={(e) => setParts({ ...parts, discount: e.target.value })} />
              <TextField size="small" label="Tax" value={parts.tax} onChange={(e) => setParts({ ...parts, tax: e.target.value })} />
            </Box>
          )}
          {problem && amount && <Alert severity="warning" sx={{ mt: 2 }}>{problem}</Alert>}
        </>}
        amount={!problem && minor ? { minor: minor.toString(), currency: o.currency } : undefined}
        reasonCodes={[...Reasons.refund]}
        confirmLabel="Refund"
        onClose={() => setOpen(false)}
        onConfirm={async ({ reasonCode, note, idempotencyKey }) => {
          if (problem || !minor) throw new Error(problem)
          await orderService.refund(o.id, {
            amount: minor.toString(), taxBreakdownProvided: split,
            subtotal: (sub ?? 0n).toString(), discount: (disc ?? 0n).toString(), tax: (tax ?? 0n).toString(),
            destination, reasonCode, memo: note,
          }, idempotencyKey)
          showNotification('Order refunded', 'success')
          onDone()
        }}
      />
    </>
  )
}
