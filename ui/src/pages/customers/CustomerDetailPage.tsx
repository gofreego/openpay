import { useNavigate, useParams } from 'react-router-dom'
import { Box, Paper, Typography } from '@mui/material'
import type { Wallet } from '../../apis/proto/openpay/v1/wallet'
import type { Payment } from '../../apis/proto/openpay/v1/payment'
import type { Order } from '../../apis/proto/openpay/v1/order'
import type { Beneficiary, Withdrawal } from '../../apis/proto/openpay/v1/withdrawal'
import {
  ApiErrorAlert, AuditPanel, DataTable, EntityLink, KeyValue, Money, PageHeader, Section, StatusChip,
} from '../../components'
import { Perm, useAsync, useConsole, usePermissions } from '../../hooks'
import { customerService, orderService, paymentService, walletService, withdrawalService } from '../../services'
import { dateTime } from '../../utils/format'
import { Money as M } from '../../utils/money'

export function CustomerDetailPage() {
  const { id = '' } = useParams()
  const customer = useAsync(() => customerService.get(id), [id])
  const c = customer.data?.customer
  if (customer.error) return <ApiErrorAlert error={customer.error} />
  if (!c) return null
  return (
    <>
      <PageHeader title={c.externalRef} subtitle={<code>{c.id}</code>} />
      <KeyValue items={[
        ['OpenAuth user', c.externalRef],
        ['Status', <StatusChip status={c.status} />],
        ['Since', dateTime(c.createdAt)],
      ]} />
      <Wallets customerId={c.id} />
      <Payments customerId={c.id} />
      <Orders customerId={c.id} />
      <Withdrawals customerId={c.id} />
      <AuditPanel resourceType="customer" resourceId={c.id} />
    </>
  )
}

/**
 * Wallets grouped by product. The server returns only what this operator
 * may see — a product operator gets their product's wallets, never another's
 * (plan.md U-D6) — so the cross-product total appears only for central ops.
 */
function Wallets({ customerId }: { customerId: string }) {
  const navigate = useNavigate()
  const { products, permissions } = useConsole()
  const wallets = useAsync(() => walletService.listForCustomer(customerId), [customerId])
  const list = wallets.data?.wallets ?? []
  const groups = new Map<string, Wallet[]>()
  for (const w of list) {
    const key = w.productId || 'platform'
    groups.set(key, [...(groups.get(key) ?? []), w])
  }
  const productName = (key: string) => key === 'platform' ? 'Platform wallets' : (products.find((p) => p.id === key)?.name ?? key)
  const total = (ws: Wallet[]) => M.add(...ws.map((w) => w.balance?.balance ?? '0'))

  return (
    <Section title="Wallets">
      {wallets.error && <ApiErrorAlert error={wallets.error} />}
      {permissions.scopeAll && list.length > 0 && (
        <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
          <Typography color="text.secondary" variant="body2">Across every product</Typography>
          <Typography variant="h6"><Money amount={total(list)} currency={list[0].currency} /></Typography>
        </Paper>
      )}
      {[...groups.entries()].map(([key, ws]) => (
        <Box key={key} sx={{ mb: 2 }}>
          <Typography variant="subtitle1" sx={{ mb: 1 }}>
            {productName(key)} · <Money amount={total(ws)} currency={ws[0].currency} />
          </Typography>
          <DataTable<Wallet>
            rows={ws}
            rowKey={(w) => w.id}
            onRowClick={(w) => navigate(`/wallets/${w.id}`)}
            columns={[
              { key: 'type', header: 'Type', render: (w) => <code>{w.walletTypeCode}</code> },
              { key: 'balance', header: 'Balance', align: 'right', render: (w) => <Money amount={w.balance?.balance} currency={w.currency} /> },
              { key: 'held', header: 'Held', align: 'right', render: (w) => <Money amount={w.balance?.held} currency={w.currency} /> },
              { key: 'available', header: 'Available', align: 'right', render: (w) => <Money amount={w.balance?.available} currency={w.currency} /> },
              { key: 'status', header: 'Status', render: (w) => <StatusChip status={w.status} /> },
            ]}
          />
        </Box>
      ))}
      {!wallets.loading && list.length === 0 && <Typography color="text.secondary">No wallets you can see.</Typography>}
    </Section>
  )
}

function Payments({ customerId }: { customerId: string }) {
  const navigate = useNavigate()
  const permissions = usePermissions()
  const payments = useAsync(() => paymentService.list({ customerId, limit: 10 }), [customerId])
  if (!permissions.can(Perm.paymentsRead)) return null
  return (
    <Section title="Recent payments">
      <DataTable<Payment>
        rows={payments.data?.payments ?? []}
        rowKey={(p) => p.id}
        loading={payments.loading}
        error={payments.error}
        onRowClick={(p) => navigate(`/payments/${p.id}`)}
        columns={[
          { key: 'id', header: 'Payment', render: (p) => <EntityLink id={p.id} /> },
          { key: 'amount', header: 'Amount', align: 'right', render: (p) => <Money amount={p.amount} currency={p.currency} /> },
          { key: 'status', header: 'Status', render: (p) => <StatusChip status={p.status} /> },
          { key: 'created', header: 'Created', render: (p) => dateTime(p.createdAt) },
        ]}
      />
    </Section>
  )
}

function Orders({ customerId }: { customerId: string }) {
  const navigate = useNavigate()
  const permissions = usePermissions()
  const orders = useAsync(() => orderService.list({ customerId, limit: 10 }), [customerId])
  if (!permissions.can(Perm.paymentsRead)) return null
  return (
    <Section title="Recent orders">
      <DataTable<Order>
        rows={orders.data?.orders ?? []}
        rowKey={(o) => o.id}
        loading={orders.loading}
        error={orders.error}
        onRowClick={(o) => navigate(`/orders/${o.id}`)}
        columns={[
          { key: 'id', header: 'Order', render: (o) => <EntityLink id={o.id} /> },
          { key: 'ref', header: 'Product ref', render: (o) => o.externalRef },
          { key: 'total', header: 'Total', align: 'right', render: (o) => <Money amount={o.total} currency={o.currency} /> },
          { key: 'status', header: 'Status', render: (o) => <StatusChip status={o.status} /> },
          { key: 'created', header: 'Created', render: (o) => dateTime(o.createdAt) },
        ]}
      />
    </Section>
  )
}

function Withdrawals({ customerId }: { customerId: string }) {
  const navigate = useNavigate()
  const permissions = usePermissions()
  const withdrawals = useAsync(() => withdrawalService.list({ customerId, limit: 10 }), [customerId])
  const beneficiaries = useAsync(() => withdrawalService.beneficiaries(customerId), [customerId])
  if (!permissions.can(Perm.withdrawalsRead)) return null
  return (
    <>
      <Section title="Bank accounts and UPI ids">
        <DataTable<Beneficiary>
          rows={beneficiaries.data?.beneficiaries ?? []}
          rowKey={(b) => b.id}
          loading={beneficiaries.loading}
          error={beneficiaries.error}
          emptyText="None registered"
          columns={[
            { key: 'dest', header: 'Destination', render: (b) => b.kind === 'vpa' ? b.vpa : `${b.accountNumberMasked} · ${b.ifsc}` },
            { key: 'name', header: 'Name given', render: (b) => b.name },
            { key: 'bank', header: 'Name at bank', render: (b) => b.nameAtBank || '—' },
            { key: 'status', header: 'Verification', render: (b) => <StatusChip status={b.status} /> },
            { key: 'why', header: '', render: (b) => b.failureReason },
          ]}
        />
      </Section>
      <Section title="Recent withdrawals">
        <DataTable<Withdrawal>
          rows={withdrawals.data?.withdrawals ?? []}
          rowKey={(x) => x.id}
          loading={withdrawals.loading}
          error={withdrawals.error}
          onRowClick={(x) => navigate(`/withdrawals/${x.id}`)}
          columns={[
            { key: 'amount', header: 'Amount', align: 'right', render: (x) => <Money amount={x.amount} currency={x.currency} /> },
            { key: 'status', header: 'Status', render: (x) => <StatusChip status={x.status} /> },
            { key: 'created', header: 'Requested', render: (x) => dateTime(x.createdAt) },
          ]}
        />
      </Section>
    </>
  )
}
