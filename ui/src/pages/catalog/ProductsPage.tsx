import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, MenuItem, TextField } from '@mui/material'
import AddIcon from '@mui/icons-material/Add'
import { useNotification } from '@gofreego/tsutils'
import type { Product } from '../../apis/proto/openpay/v1/product'
import { ProductStatus } from '../../apis/proto/openpay/v1/product'
import { ConfirmAction, DataTable, PageHeader, StatusChip } from '../../components'
import { Perm, useAsync, useConsole, usePermissions } from '../../hooks'
import { productService } from '../../services'
import { dateTime } from '../../utils/format'

const PAGE = 25

export function ProductsPage() {
  const navigate = useNavigate()
  const permissions = usePermissions()
  const { reload: reloadConsole } = useConsole()
  const { showNotification } = useNotification()
  const [page, setPage] = useState(0)
  const [search, setSearch] = useState('')
  const [status, setStatus] = useState<ProductStatus | ''>('')
  const [creating, setCreating] = useState(false)
  const [draft, setDraft] = useState({ code: '', name: '', defaultCurrency: 'INR' })

  const products = useAsync(
    () => productService.list({ limit: PAGE, offset: page * PAGE, search, status: status || undefined }),
    [page, search, status],
  )

  return (
    <>
      <PageHeader
        help="products"
        title="Products"
        subtitle="Our own apps. Every wallet, order and rupee of revenue belongs to one."
        action={permissions.canPlatform(Perm.productsWrite) && (
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => setCreating(true)}>New product</Button>
        )}
      />
      <DataTable<Product>
        rows={products.data?.products ?? []}
        rowKey={(p) => p.id}
        loading={products.loading}
        error={products.error}
        onRowClick={(p) => navigate(`/products/${p.id}`)}
        filters={<>
          <TextField size="small" label="Search code or name" value={search} onChange={(e) => { setSearch(e.target.value); setPage(0) }} />
          <TextField size="small" select label="Status" value={status} sx={{ minWidth: 160 }}
            onChange={(e) => { setStatus(e.target.value as ProductStatus | ''); setPage(0) }}>
            <MenuItem value="">Any</MenuItem>
            <MenuItem value={ProductStatus.PRODUCT_STATUS_ACTIVE}>Active</MenuItem>
            <MenuItem value={ProductStatus.PRODUCT_STATUS_SUSPENDED}>Suspended</MenuItem>
          </TextField>
        </>}
        pagination={{ mode: 'offset', page, pageSize: PAGE, total: Number.parseInt(products.data?.total ?? '0', 10), onPageChange: setPage }}
        columns={[
          { key: 'code', header: 'Code', render: (p) => <code>{p.code}</code> },
          { key: 'name', header: 'Name', render: (p) => p.name },
          { key: 'currency', header: 'Currency', render: (p) => p.defaultCurrency },
          { key: 'refunds', header: 'Order refunds go to', render: (p) => p.refundDestination },
          { key: 'status', header: 'Status', render: (p) => <StatusChip status={p.status} /> },
          { key: 'created', header: 'Created', render: (p) => dateTime(p.createdAt) },
        ]}
      />
      <ConfirmAction
        open={creating}
        title="Register a product"
        description={<>
          The code is permanent: it becomes part of ledger account names such as{' '}
          <code>income:{draft.code || 'code'}:product_sales</code>. Registering also opens the product's MAIN and BONUS wallet types.
          <TextField fullWidth sx={{ mt: 2 }} label="Code (lowercase, permanent)" value={draft.code}
            onChange={(e) => setDraft({ ...draft, code: e.target.value.toLowerCase() })} />
          <TextField fullWidth sx={{ mt: 2 }} label="Name" value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
          <TextField fullWidth sx={{ mt: 2 }} label="Currency" value={draft.defaultCurrency} disabled helperText="INR only for now" />
        </>}
        requireNote={false}
        confirmLabel="Register"
        onClose={() => setCreating(false)}
        onConfirm={async ({ idempotencyKey }) => {
          const res = await productService.create(draft, idempotencyKey)
          showNotification(`Registered ${res.product?.name}`, 'success')
          setDraft({ code: '', name: '', defaultCurrency: 'INR' })
          products.reload()
          reloadConsole()
        }}
      />
    </>
  )
}
