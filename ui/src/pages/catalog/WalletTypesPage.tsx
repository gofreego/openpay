import { useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import {
  Alert, Box, Button, Checkbox, FormControlLabel, MenuItem, TextField, Typography,
} from '@mui/material'
import AddIcon from '@mui/icons-material/Add'
import { useNotification } from '@gofreego/tsutils'
import {
  ExpiryPolicy, WalletScope, type CreateWalletTypeRequest, type WalletCapabilities, type WalletType,
} from '../../apis/proto/openpay/v1/wallet_type'
import { ConfirmAction, DataTable, PageHeader, StatusChip } from '../../components'
import { Perm, useAsync, useConsole, usePermissions } from '../../hooks'
import { walletTypeService } from '../../services'
import { capabilitySummary } from './walletTypeSummary'
import { LimitsFields } from './LimitsFields'
import { limitsDraft, toLimits } from './limits'

export function WalletTypesPage() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const { products, selectedProduct } = useConsole()
  const permissions = usePermissions()
  const productId = params.get('product') ?? (selectedProduct === 'all' ? '' : selectedProduct)
  // The API lists one product's types (plus the platform-wide ones), so
  // "All products" asks the operator to pick one rather than sending none.
  const types = useAsync(() => productId ? walletTypeService.list(productId) : Promise.resolve(null), [productId])
  const [creating, setCreating] = useState(false)
  const productName = (id: string) => products.find((p) => p.id === id)?.code ?? id

  return (
    <>
      <PageHeader
        help="wallet-types"
        title="Wallet types"
        subtitle="The rules a balance lives under: who may fund, spend, move or cash it out."
        action={permissions.canPlatform(Perm.walletTypesWrite) && (
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => setCreating(true)}>New wallet type</Button>
        )}
      />
      {!productId ? (
        <Alert severity="info">Choose a product in the product picker to see its wallet types, alongside the platform-wide ones.</Alert>
      ) : (
      <DataTable<WalletType>
        rows={types.data?.walletTypes ?? []}
        rowKey={(t) => t.id}
        loading={types.loading}
        error={types.error}
        onRowClick={(t) => navigate(`/wallet-types/${t.id}`)}
        columns={[
          { key: 'code', header: 'Code', render: (t) => <code>{t.code}</code> },
          { key: 'owner', header: 'Belongs to', render: (t) => t.scope === WalletScope.WALLET_SCOPE_PLATFORM ? 'Platform (every product)' : productName(t.productId) },
          { key: 'means', header: 'What it means', render: (t) => (
            <Box>
              {t.capabilities?.withdrawable && <Typography variant="caption" color="warning.main" fontWeight={700}>CASH-OUT ENABLED · </Typography>}
              {capabilitySummary(t)}
            </Box>
          ) },
          { key: 'status', header: 'Status', render: (t) => <StatusChip status={t.status} /> },
        ]}
      />
      )}
      <CreateWalletType open={creating} onClose={() => setCreating(false)} onCreated={(t) => navigate(`/wallet-types/${t.id}`)} defaultProduct={productId} />
    </>
  )
}

const NO_CAPS: WalletCapabilities = {
  fundable: false, grantable: false, withdrawable: false, transferable: false, refundableToSource: false, allowNegative: false,
}

function CreateWalletType({ open, onClose, onCreated, defaultProduct }: {
  open: boolean; onClose: () => void; onCreated: (t: WalletType) => void; defaultProduct: string
}) {
  const { products } = useConsole()
  const permissions = usePermissions()
  const { showNotification } = useNotification()
  const [productId, setProductId] = useState(defaultProduct)
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  const [caps, setCaps] = useState<WalletCapabilities>(NO_CAPS)
  const [expiry, setExpiry] = useState<ExpiryPolicy>(ExpiryPolicy.EXPIRY_POLICY_NONE)
  const [expiryDays, setExpiryDays] = useState('')
  const [limits, setLimits] = useState(limitsDraft(undefined, 'INR'))
  const [approvalRef, setApprovalRef] = useState('')
  const [formError, setFormError] = useState<string | null>(null)

  const canApproveWithdrawal = permissions.canPlatform(Perm.walletTypesApproveWithdrawal)
  const scope = productId ? WalletScope.WALLET_SCOPE_PRODUCT : WalletScope.WALLET_SCOPE_PLATFORM
  const toggle = (key: keyof WalletCapabilities) => (_: unknown, checked: boolean) => setCaps({ ...caps, [key]: checked })

  return (
    <ConfirmAction
      open={open}
      title="New wallet type"
      description={<>
        <Alert severity="info" sx={{ mb: 2 }}>
          Scope, currency and capabilities cannot change once wallets of this type exist — balances already held must not have
          their rules rewritten underneath them.
        </Alert>
        <TextField fullWidth select label="Belongs to" value={productId} onChange={(e) => setProductId(e.target.value)}>
          <MenuItem value="">Platform — spendable in every product</MenuItem>
          {products.map((p) => <MenuItem key={p.id} value={p.id}>{p.name} ({p.code})</MenuItem>)}
        </TextField>
        <Box sx={{ display: 'flex', gap: 2, mt: 2 }}>
          <TextField label="Code (permanent)" value={code} onChange={(e) => setCode(e.target.value.toUpperCase())} />
          <TextField label="Name" value={name} onChange={(e) => setName(e.target.value)} sx={{ flex: 1 }} />
        </Box>
        <Typography variant="subtitle2" sx={{ mt: 2 }}>Capabilities</Typography>
        <Box sx={{ display: 'grid', gridTemplateColumns: '1fr 1fr' }}>
          <FormControlLabel control={<Checkbox checked={caps.fundable} onChange={toggle('fundable')} />} label="Fundable (real money top-ups)" />
          <FormControlLabel control={<Checkbox checked={caps.grantable} onChange={toggle('grantable')} />} label="Grantable (promotions)" />
          <FormControlLabel control={<Checkbox checked={caps.transferable} onChange={toggle('transferable')} />} label="Transferable between customers" />
          <FormControlLabel control={<Checkbox checked={caps.refundableToSource} onChange={toggle('refundableToSource')} />} label="Refundable to the card" />
          <FormControlLabel
            control={<Checkbox checked={caps.withdrawable} onChange={toggle('withdrawable')} disabled={!canApproveWithdrawal} color="warning" />}
            label={canApproveWithdrawal ? 'Withdrawable (cash-out) — compliance decision' : 'Withdrawable — needs openpay:wallet_types:approve_withdrawal'}
          />
        </Box>
        {caps.withdrawable && (
          <Alert severity="warning" sx={{ mt: 1 }}>
            Cash-out turns this into a prepaid payment instrument in regulatory terms (plan.md D10, Q1). Name the compliance
            approval this rests on; it is recorded with your name.
            <TextField fullWidth size="small" sx={{ mt: 1 }} label="Compliance approval reference" value={approvalRef}
              onChange={(e) => setApprovalRef(e.target.value)} />
          </Alert>
        )}
        <Box sx={{ display: 'flex', gap: 2, mt: 2 }}>
          <TextField select label="Expiry" value={expiry} onChange={(e) => setExpiry(e.target.value as ExpiryPolicy)} sx={{ minWidth: 220 }}>
            <MenuItem value={ExpiryPolicy.EXPIRY_POLICY_NONE}>Never expires</MenuItem>
            <MenuItem value={ExpiryPolicy.EXPIRY_POLICY_ROLLING}>Lapses after inactivity</MenuItem>
            <MenuItem value={ExpiryPolicy.EXPIRY_POLICY_FIXED}>Lapses after a fixed period</MenuItem>
          </TextField>
          {expiry !== ExpiryPolicy.EXPIRY_POLICY_NONE && (
            <TextField label="Days" value={expiryDays} onChange={(e) => setExpiryDays(e.target.value.replace(/\D/g, ''))} />
          )}
        </Box>
        <Typography variant="subtitle2" sx={{ mt: 2 }}>Limits</Typography>
        <LimitsFields draft={limits} onChange={setLimits} withdrawable={caps.withdrawable} currency="INR" />
        <Alert severity="success" icon={false} sx={{ mt: 2 }}>
          {capabilitySummary({ capabilities: caps, scope, expiryPolicy: expiry, code })}
        </Alert>
        {formError && <Alert severity="error" sx={{ mt: 2 }}>{formError}</Alert>}
      </>}
      requireNote={false}
      confirmLabel="Create"
      onClose={onClose}
      onConfirm={async ({ idempotencyKey }) => {
        setFormError(null)
        let parsedLimits
        try {
          parsedLimits = toLimits(limits, 'INR')
        } catch (err) {
          setFormError((err as Error).message)
          throw err
        }
        const body: CreateWalletTypeRequest = {
          productId, code, name, currency: 'INR', capabilities: caps, expiryPolicy: expiry,
          expiryDays: Number.parseInt(expiryDays || '0', 10), limits: parsedLimits, withdrawalApprovalRef: approvalRef,
        }
        const res = await walletTypeService.create(body, idempotencyKey)
        showNotification(`Created ${res.walletType?.code}`, 'success')
        if (res.walletType) onCreated(res.walletType)
      }}
    />
  )
}
