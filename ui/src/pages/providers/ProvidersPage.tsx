import { useState } from 'react'
import { Alert, Box, Button, Card, CardContent, Chip, Grid, Typography } from '@mui/material'
import { useNotification } from '@gofreego/tsutils'
import { ProviderOverride, type ProviderStatus } from '../../apis/proto/openpay/v1/provider'
import { ApiErrorAlert, ConfirmAction, PageHeader } from '../../components'
import { Perm, useAsync, usePermissions } from '../../hooks'
import { providerService, reportService } from '../../services'
import { dateTime, isoDaysAgo } from '../../utils/format'

/**
 * Which provider takes new payments, and why: priority order, skipping any
 * disabled or with an open circuit, unless one is forced.
 */
function takingTraffic(providers: ProviderStatus[]): { name: string; why: string } | null {
  const forced = providers.find((p) => p.forced)
  if (forced) return { name: forced.name, why: `forced by an operator: ${forced.overrideReason}` }
  const sorted = [...providers].sort((a, b) => a.priority - b.priority)
  const first = sorted.find((p) => !p.disabled && p.healthy)
  if (!first) return null
  const skipped = sorted.slice(0, sorted.indexOf(first))
  return {
    name: first.name,
    why: skipped.length ? `${skipped.map((p) => `${p.name} is ${p.disabled ? 'disabled' : 'unhealthy'}`).join(', ')}` : 'first in priority order',
  }
}

export function ProvidersPage() {
  const permissions = usePermissions()
  const { showNotification } = useNotification()
  const providers = useAsync(() => providerService.list(), [])
  const stats = useAsync(() => reportService.providerStats(isoDaysAgo(1), new Date().toISOString()), [])
  const [change, setChange] = useState<{ provider: ProviderStatus; to: ProviderOverride } | null>(null)
  const list = providers.data?.providers ?? []
  const traffic = takingTraffic(list)
  const canManage = permissions.canPlatform(Perm.providersManage)

  const labels: Record<string, string> = {
    [ProviderOverride.PROVIDER_OVERRIDE_DISABLED]: 'Disable',
    [ProviderOverride.PROVIDER_OVERRIDE_FORCED]: 'Force all traffic here',
    [ProviderOverride.PROVIDER_OVERRIDE_NONE]: 'Restore normal routing',
  }

  return (
    <>
      <PageHeader title="Providers" subtitle="Health, routing and the kill-switch. Credentials live in the secret store and are never shown here." />
      {providers.error && <ApiErrorAlert error={providers.error} />}
      {list.length > 0 && (traffic
        ? <Alert severity="info" sx={{ mb: 2 }}>New payments go to <b>{traffic.name}</b> — {traffic.why}.</Alert>
        : <Alert severity="error" sx={{ mb: 2 }}>No provider can take new payments. Checkouts are failing.</Alert>)}
      <Grid container spacing={2}>
        {list.map((p) => {
          const s = stats.data?.providers.find((x) => x.provider === p.name)
          return (
            <Grid key={p.name} size={{ xs: 12, md: 6 }}>
              <Card variant="outlined" sx={{ borderColor: p.forced ? 'warning.main' : p.disabled || !p.healthy ? 'error.main' : undefined }}>
                <CardContent>
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                    <Typography variant="h6" sx={{ flex: 1 }}>{p.name}</Typography>
                    <Chip size="small" label={`priority ${p.priority}`} />
                    <Chip size="small" color={p.healthy ? 'success' : 'error'} label={p.healthy ? 'healthy' : 'circuit open'} />
                    {p.disabled && <Chip size="small" color="error" label="disabled" />}
                    {p.forced && <Chip size="small" color="warning" label="forced" />}
                  </Box>
                  {!p.healthy && <Typography variant="body2">Circuit open until {dateTime(p.circuitOpenUntil)} after {p.consecutiveFailures} failures.</Typography>}
                  {p.lastError && <Typography variant="body2" color="error">Last error: {p.lastError}</Typography>}
                  {p.overrideReason && <Typography variant="body2">Override: {p.overrideReason}</Typography>}
                  {s && (
                    <Typography variant="body2" sx={{ mt: 1 }}>
                      Last 24h: {(s.successRateBps / 100).toString()}% success over {Number.parseInt(s.captured, 10) + Number.parseInt(s.failed, 10) + Number.parseInt(s.expired, 10) + Number.parseInt(s.cancelled, 10)} finished attempts
                      {s.topFailures.length > 0 && ` · top failure: ${s.topFailures[0].code} ×${s.topFailures[0].count}`}
                    </Typography>
                  )}
                  {canManage && (
                    <Box sx={{ display: 'flex', gap: 1, mt: 2 }}>
                      {!p.disabled && <Button size="small" color="error" onClick={() => setChange({ provider: p, to: ProviderOverride.PROVIDER_OVERRIDE_DISABLED })}>Disable</Button>}
                      {!p.forced && <Button size="small" color="warning" onClick={() => setChange({ provider: p, to: ProviderOverride.PROVIDER_OVERRIDE_FORCED })}>Force</Button>}
                      {(p.disabled || p.forced) && <Button size="small" onClick={() => setChange({ provider: p, to: ProviderOverride.PROVIDER_OVERRIDE_NONE })}>Restore</Button>}
                    </Box>
                  )}
                </CardContent>
              </Card>
            </Grid>
          )
        })}
      </Grid>
      <ConfirmAction
        open={change !== null}
        title={change ? `${labels[change.to]}: ${change.provider.name}` : ''}
        destructive={change?.to !== ProviderOverride.PROVIDER_OVERRIDE_NONE}
        description={<>
          Takes effect on the next payment attempt in every OpenPay process, without a deploy. Payments already started stay with
          their provider. {change?.to === ProviderOverride.PROVIDER_OVERRIDE_DISABLED && list.filter((p) => !p.disabled).length <= 1 &&
            <b>This is the only provider taking traffic: disabling it stops all payments.</b>}
        </>}
        requireNote
        confirmLabel={change ? labels[change.to] : ''}
        onClose={() => setChange(null)}
        onConfirm={async ({ note, idempotencyKey }) => {
          await providerService.override(change!.provider.name, change!.to, note, idempotencyKey)
          showNotification('Routing updated', 'success')
          providers.reload()
        }}
      />
    </>
  )
}
