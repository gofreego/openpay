import { useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Chip, MenuItem, TextField } from '@mui/material'
import { PaymentStatus, type Payment } from '../../apis/proto/openpay/v1/payment'
import { DataTable, EntityLink, Money, PageHeader, StatusChip } from '../../components'
import { useAsync, useConsole } from '../../hooks'
import { paymentService } from '../../services'
import { dateTime, enumLabel } from '../../utils/format'
import { toCsv, saveText } from '../../utils/csv'
import { Money as M } from '../../utils/money'

const PAGE = 25

// Saved views ops reach for most. "Stuck" is open payments: the poller should
// close them within minutes, so anything old here needs a sync.
const PRESETS: { label: string; status: PaymentStatus | '' }[] = [
  { label: 'All', status: '' },
  { label: 'Stuck (pending)', status: PaymentStatus.PAYMENT_STATUS_PENDING },
  { label: 'Authorized', status: PaymentStatus.PAYMENT_STATUS_AUTHORIZED },
  { label: 'Failed', status: PaymentStatus.PAYMENT_STATUS_FAILED },
  { label: 'Disputed', status: PaymentStatus.PAYMENT_STATUS_DISPUTED },
]

export function PaymentsPage() {
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const { selectedProduct } = useConsole()
  const [page, setPage] = useState(0)
  const status = (params.get('status') ?? '') as PaymentStatus | ''
  const customerId = params.get('customer') ?? ''
  const productId = selectedProduct === 'all' ? undefined : selectedProduct

  const payments = useAsync(() => paymentService.list({
    limit: PAGE, offset: page * PAGE, productId, customerId: customerId || undefined, status: status || undefined,
  }), [page, status, customerId, productId])

  const setFilter = (key: string, value: string) => {
    const next = new URLSearchParams(params)
    if (value) next.set(key, value); else next.delete(key)
    setParams(next)
    setPage(0)
  }

  const rows = payments.data?.payments ?? []
  return (
    <>
      <PageHeader title="Payments" subtitle="Every checkout, and where its money went." />
      <DataTable<Payment>
        rows={rows}
        rowKey={(p) => p.id}
        loading={payments.loading}
        error={payments.error}
        onRowClick={(p) => navigate(`/payments/${p.id}`)}
        onExport={() => saveText('payments.csv', toCsv(
          ['id', 'product', 'customer', 'purpose', 'amount', 'captured', 'currency', 'status', 'provider', 'created_at'],
          rows.map((p) => [p.id, p.productId, p.customerId, p.purpose, M.toMajor(p.amount, p.currency),
            M.toMajor(p.capturedAmount || '0', p.currency), p.currency, p.status, p.provider, p.createdAt ?? '']),
        ))}
        filters={<>
          {PRESETS.map((preset) => (
            <Chip key={preset.label} label={preset.label} color={status === preset.status ? 'primary' : 'default'}
              onClick={() => setFilter('status', preset.status)} />
          ))}
          <TextField size="small" select label="Status" value={status} sx={{ minWidth: 180 }} onChange={(e) => setFilter('status', e.target.value)}>
            <MenuItem value="">Any</MenuItem>
            {Object.values(PaymentStatus).filter((s) => s !== PaymentStatus.PAYMENT_STATUS_UNSPECIFIED && s !== PaymentStatus.UNRECOGNIZED)
              .map((s) => <MenuItem key={s} value={s}>{enumLabel(s, 'PAYMENT_STATUS_')}</MenuItem>)}
          </TextField>
          <TextField size="small" label="Customer id" value={customerId} onChange={(e) => setFilter('customer', e.target.value.trim())} placeholder="cus_…" />
        </>}
        pagination={{ mode: 'offset', page, pageSize: PAGE, total: Number.parseInt(payments.data?.total ?? '0', 10), onPageChange: setPage }}
        columns={[
          { key: 'id', header: 'Payment', render: (p) => <EntityLink id={p.id} /> },
          { key: 'purpose', header: 'For', render: (p) => enumLabel(p.purpose, 'PAYMENT_PURPOSE_') },
          { key: 'amount', header: 'Amount', align: 'right', render: (p) => <Money amount={p.amount} currency={p.currency} /> },
          { key: 'status', header: 'Status', render: (p) => <StatusChip status={p.status} /> },
          { key: 'provider', header: 'Provider', render: (p) => p.provider || '—' },
          { key: 'customer', header: 'Customer', render: (p) => <EntityLink id={p.customerId} /> },
          { key: 'created', header: 'Created', render: (p) => dateTime(p.createdAt) },
        ]}
      />
    </>
  )
}
