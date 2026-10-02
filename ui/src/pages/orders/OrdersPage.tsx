import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { MenuItem, TextField } from '@mui/material'
import { OrderStatus, type Order } from '../../apis/proto/openpay/v1/order'
import { DataTable, EntityLink, Money, PageHeader, StatusChip } from '../../components'
import { useAsync, useConsole } from '../../hooks'
import { orderService } from '../../services'
import { dateTime, enumLabel } from '../../utils/format'

const PAGE = 25

export function OrdersPage() {
  const navigate = useNavigate()
  const { selectedProduct } = useConsole()
  const [page, setPage] = useState(0)
  const [status, setStatus] = useState<OrderStatus | ''>('')
  const productId = selectedProduct === 'all' ? undefined : selectedProduct
  const orders = useAsync(() => orderService.list({ limit: PAGE, offset: page * PAGE, productId, status: status || undefined }),
    [page, status, productId])

  return (
    <>
      <PageHeader title="Orders" help="orders" subtitle="Purchases, paid from wallets, the card, or both." />
      <DataTable<Order>
        rows={orders.data?.orders ?? []}
        rowKey={(o) => o.id}
        loading={orders.loading}
        error={orders.error}
        onRowClick={(o) => navigate(`/orders/${o.id}`)}
        filters={
          <TextField size="small" select label="Status" value={status} sx={{ minWidth: 180 }} onChange={(e) => { setStatus(e.target.value as OrderStatus | ''); setPage(0) }}>
            <MenuItem value="">Any</MenuItem>
            {Object.values(OrderStatus).filter((s) => s !== OrderStatus.ORDER_STATUS_UNSPECIFIED && s !== OrderStatus.UNRECOGNIZED)
              .map((s) => <MenuItem key={s} value={s}>{enumLabel(s, 'ORDER_STATUS_')}</MenuItem>)}
          </TextField>
        }
        pagination={{ mode: 'offset', page, pageSize: PAGE, total: Number.parseInt(orders.data?.total ?? '0', 10), onPageChange: setPage }}
        columns={[
          { key: 'id', header: 'Order', render: (o) => <EntityLink id={o.id} /> },
          { key: 'ref', header: 'Product ref', render: (o) => o.externalRef },
          { key: 'product', header: 'Product', render: (o) => <EntityLink id={o.productId} /> },
          { key: 'total', header: 'Total', align: 'right', render: (o) => <Money amount={o.total} currency={o.currency} /> },
          { key: 'card', header: 'On card', align: 'right', render: (o) => <Money amount={o.gatewayAmount} currency={o.currency} /> },
          { key: 'status', header: 'Status', render: (o) => <StatusChip status={o.status} /> },
          { key: 'created', header: 'Created', render: (o) => dateTime(o.createdAt) },
        ]}
      />
    </>
  )
}
