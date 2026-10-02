import { useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router-dom'
import { MenuItem, TextField } from '@mui/material'
import { RefundStatus, type Refund } from '../../apis/proto/openpay/v1/payment'
import { ApiErrorAlert, DataTable, EntityLink, Money, PageHeader, StatusChip } from '../../components'
import { useAsync, useConsole } from '../../hooks'
import { refundService } from '../../services'
import { dateTime, enumLabel } from '../../utils/format'

const PAGE = 25

export function RefundsPage() {
  const navigate = useNavigate()
  const { selectedProduct } = useConsole()
  const [page, setPage] = useState(0)
  const [status, setStatus] = useState<RefundStatus | ''>('')
  const productId = selectedProduct === 'all' ? undefined : selectedProduct
  const refunds = useAsync(() => refundService.search({ status: status || undefined, productId, limit: PAGE, offset: page * PAGE }),
    [status, productId, page])

  return (
    <>
      <PageHeader title="Refunds" help="refunds" subtitle="Money going back to cards and banks. Initiated and pending ones are still with the provider." />
      <DataTable<Refund>
        rows={refunds.data?.refunds ?? []}
        rowKey={(r) => r.id}
        loading={refunds.loading}
        error={refunds.error}
        onRowClick={(r) => navigate(`/payments/${r.paymentId}`)}
        filters={
          <TextField size="small" select label="Status" value={status} sx={{ minWidth: 160 }} onChange={(e) => { setStatus(e.target.value as RefundStatus | ''); setPage(0) }}>
            <MenuItem value="">Any</MenuItem>
            {Object.values(RefundStatus).filter((s) => s !== RefundStatus.REFUND_STATUS_UNSPECIFIED && s !== RefundStatus.UNRECOGNIZED)
              .map((s) => <MenuItem key={s} value={s}>{enumLabel(s, 'REFUND_STATUS_')}</MenuItem>)}
          </TextField>
        }
        pagination={{ mode: 'offset', page, pageSize: PAGE, total: Number.parseInt(refunds.data?.total ?? '0', 10), onPageChange: setPage }}
        columns={[
          { key: 'id', header: 'Refund', render: (r) => <code>{r.id}</code> },
          { key: 'payment', header: 'Payment', render: (r) => <EntityLink id={r.paymentId} /> },
          { key: 'amount', header: 'Amount', align: 'right', render: (r) => <Money amount={r.amount} currency={r.currency} /> },
          { key: 'status', header: 'Status', render: (r) => <StatusChip status={r.status} /> },
          { key: 'why', header: 'Reason', render: (r) => r.reasonCode },
          { key: 'failure', header: 'Failure', render: (r) => r.failureReason },
          { key: 'when', header: 'Requested', render: (r) => dateTime(r.createdAt) },
        ]}
      />
    </>
  )
}

/** RefundRedirect opens a refund's payment, where refunds are shown in context. */
export function RefundRedirect() {
  const { id = '' } = useParams()
  const refund = useAsync(() => refundService.get(id), [id])
  if (refund.error) return <ApiErrorAlert error={refund.error} />
  return refund.data?.refund ? <Navigate to={`/payments/${refund.data.refund.paymentId}`} replace /> : null
}
