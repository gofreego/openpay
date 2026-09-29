import { useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Alert, Box, Button, IconButton, MenuItem, TextField, Tooltip, Typography } from '@mui/material'
import ContentCopyIcon from '@mui/icons-material/ContentCopy'
import { useNotification } from '@gofreego/tsutils'
import { ProductStatus, type Product } from '../../apis/proto/openpay/v1/product'
import type { ServiceCredential } from '../../apis/proto/openpay/v1/credential'
import type { WalletType } from '../../apis/proto/openpay/v1/wallet_type'
import { ApiErrorAlert, AuditPanel, ConfirmAction, DataTable, KeyValue, PageHeader, Section, StatusChip } from '../../components'
import { Perm, useAsync, useConsole, usePermissions } from '../../hooks'
import { credentialService, productService, walletTypeService } from '../../services'
import { dateTime } from '../../utils/format'
import { capabilitySummary } from './walletTypeSummary'

export function ProductDetailPage() {
  const { id = '' } = useParams()
  const product = useAsync(() => productService.get(id), [id])
  const p = product.data?.product

  if (product.error) return <ApiErrorAlert error={product.error} />
  if (!p) return null
  return (
    <>
      <PageHeader title={p.name} subtitle={<code>{p.code}</code>} action={<EditProduct product={p} onSaved={product.reload} />} />
      <KeyValue items={[
        ['Id', <code>{p.id}</code>],
        ['Status', <StatusChip status={p.status} />],
        ['Currency', p.defaultCurrency],
        ['Order refunds go to', p.refundDestination === 'wallet' ? 'Store credit (wallet)' : 'Back to how it was paid'],
        ['Created', dateTime(p.createdAt)],
      ]} />
      <WalletTypes productId={p.id} />
      <Credentials productId={p.id} />
      <AuditPanel resourceType="product" resourceId={p.id} />
    </>
  )
}

function EditProduct({ product, onSaved }: { product: Product; onSaved: () => void }) {
  const permissions = usePermissions()
  const { reload } = useConsole()
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState({ name: product.name, status: product.status, refundDestination: product.refundDestination })
  if (!permissions.canPlatform(Perm.productsWrite)) return null

  const suspending = draft.status === ProductStatus.PRODUCT_STATUS_SUSPENDED && product.status !== draft.status
  return (
    <>
      <Button variant="outlined" onClick={() => { setDraft({ name: product.name, status: product.status, refundDestination: product.refundDestination }); setOpen(true) }}>
        Edit
      </Button>
      <ConfirmAction
        open={open}
        title={`Edit ${product.name}`}
        destructive={suspending}
        description={<>
          <TextField fullWidth sx={{ mt: 1 }} label="Name" value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
          <TextField fullWidth select sx={{ mt: 2 }} label="Status" value={draft.status}
            onChange={(e) => setDraft({ ...draft, status: e.target.value as ProductStatus })}>
            <MenuItem value={ProductStatus.PRODUCT_STATUS_ACTIVE}>Active</MenuItem>
            <MenuItem value={ProductStatus.PRODUCT_STATUS_SUSPENDED}>Suspended</MenuItem>
          </TextField>
          <TextField fullWidth select sx={{ mt: 2 }} label="Order refunds go to" value={draft.refundDestination}
            onChange={(e) => setDraft({ ...draft, refundDestination: e.target.value })}>
            <MenuItem value="source">Back to how it was paid</MenuItem>
            <MenuItem value="wallet">Store credit (wallet)</MenuItem>
          </TextField>
          {suspending && (
            <Alert severity="warning" sx={{ mt: 2 }}>
              Suspending stops this product's credentials from authenticating at once: its checkouts, top-ups and orders stop.
            </Alert>
          )}
        </>}
        requireNote={false}
        confirmLabel="Save"
        onClose={() => setOpen(false)}
        onConfirm={async ({ idempotencyKey }) => {
          await productService.update(product.id, draft, idempotencyKey)
          onSaved()
          reload()
        }}
      />
    </>
  )
}

function WalletTypes({ productId }: { productId: string }) {
  const navigate = useNavigate()
  const types = useAsync(() => walletTypeService.list(productId), [productId])
  return (
    <Section title="Wallet types" action={<Button size="small" onClick={() => navigate(`/wallet-types?product=${productId}`)}>Manage</Button>}>
      <DataTable<WalletType>
        rows={types.data?.walletTypes ?? []}
        rowKey={(t) => t.id}
        loading={types.loading}
        error={types.error}
        onRowClick={(t) => navigate(`/wallet-types/${t.id}`)}
        columns={[
          { key: 'code', header: 'Code', render: (t) => <code>{t.code}</code> },
          { key: 'name', header: 'Name', render: (t) => t.name },
          { key: 'means', header: 'What it means', render: (t) => capabilitySummary(t) },
          { key: 'status', header: 'Status', render: (t) => <StatusChip status={t.status} /> },
        ]}
      />
    </Section>
  )
}

function Credentials({ productId }: { productId: string }) {
  const permissions = usePermissions()
  const { showNotification } = useNotification()
  const creds = useAsync(() => credentialService.list(productId), [productId])
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [revoking, setRevoking] = useState<ServiceCredential | null>(null)
  // The secret exists in the browser only until this panel is dismissed.
  const [issued, setIssued] = useState<{ token: string; name: string } | null>(null)
  const canWrite = permissions.canPlatform(Perm.credentialsWrite)

  return (
    <Section title="Service credentials" action={canWrite && <Button size="small" onClick={() => { setName(''); setCreating(true) }}>Issue credential</Button>}>
      {issued && (
        <Alert severity="warning" sx={{ mb: 2 }} onClose={() => setIssued(null)}>
          <Typography fontWeight={600}>Copy this now — it will never be shown again.</Typography>
          <Typography variant="body2">Give it to the {issued.name} backend as <code>Authorization: Bearer &lt;token&gt;</code>.</Typography>
          <Box sx={{ display: 'flex', alignItems: 'center', mt: 1, fontFamily: 'monospace', wordBreak: 'break-all' }}>
            {issued.token}
            <Tooltip title="Copy">
              <IconButton size="small" onClick={() => void navigator.clipboard?.writeText(issued.token)}><ContentCopyIcon fontSize="small" /></IconButton>
            </Tooltip>
          </Box>
        </Alert>
      )}
      <DataTable<ServiceCredential>
        rows={creds.data?.credentials ?? []}
        rowKey={(c) => c.id}
        loading={creds.loading}
        error={creds.error}
        emptyText="No credentials: this product's backend cannot call OpenPay yet"
        columns={[
          { key: 'name', header: 'Name', render: (c) => c.name },
          { key: 'key', header: 'Key id', render: (c) => <code>{c.keyId}</code> },
          { key: 'status', header: 'Status', render: (c) => <StatusChip status={c.status} /> },
          { key: 'used', header: 'Last used', render: (c) => dateTime(c.lastUsedAt) },
          { key: 'created', header: 'Issued', render: (c) => dateTime(c.createdAt) },
          {
            key: 'actions', header: '', align: 'right', render: (c) => canWrite && !c.revokedAt && (
              <Button size="small" color="error" onClick={() => setRevoking(c)}>Revoke</Button>
            ),
          },
        ]}
      />
      <ConfirmAction
        open={creating}
        title="Issue a service credential"
        description={<>
          The secret is shown once and stored only as a hash. To rotate, issue a new one, deploy it, then revoke the old.
          <TextField fullWidth sx={{ mt: 2 }} label="Name (who uses it)" value={name} onChange={(e) => setName(e.target.value)} />
        </>}
        requireNote={false}
        confirmLabel="Issue"
        onClose={() => setCreating(false)}
        onConfirm={async ({ idempotencyKey }) => {
          const res = await credentialService.create(productId, name, idempotencyKey)
          setIssued({ token: `${res.credential?.keyId}.${res.secret}`, name })
          creds.reload()
        }}
      />
      <ConfirmAction
        open={revoking !== null}
        title={`Revoke ${revoking?.name}`}
        destructive
        description={<>
          Requests with key <code>{revoking?.keyId}</code> fail from the next call. Make sure its replacement is deployed first.
        </>}
        requireNote={false}
        confirmLabel="Revoke"
        onClose={() => setRevoking(null)}
        onConfirm={async ({ idempotencyKey }) => {
          await credentialService.revoke(revoking!.id, idempotencyKey)
          showNotification('Credential revoked', 'success')
          creds.reload()
        }}
      />
    </Section>
  )
}
