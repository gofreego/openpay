import { Card, CardContent, Grid, Typography } from '@mui/material'
import { PageHeader } from '../../components'
import { useConsole } from '../../hooks'

/**
 * DashboardPage is the landing page. KPIs (success rate by provider, float
 * held, suspense, drift, stuck payments) arrive in UI phase U6; for now it
 * says who you are and what you can reach.
 */
export function DashboardPage() {
  const { me, permissions, products, selectedProduct } = useConsole()
  const selected = selectedProduct === 'all' ? 'All products' : products.find((p) => p.id === selectedProduct)?.name

  return (
    <>
      <PageHeader title="OpenPay" subtitle="Payments, wallets and the ledger behind them" />
      <Grid container spacing={2}>
        <Grid size={{ xs: 12, md: 4 }}>
          <Card variant="outlined">
            <CardContent>
              <Typography variant="overline" color="text.secondary">Signed in as</Typography>
              <Typography variant="h6">{me?.userId}</Typography>
              <Typography color="text.secondary">{permissions.scopeAll ? 'Central ops — every product' : 'Product ops'}</Typography>
            </CardContent>
          </Card>
        </Grid>
        <Grid size={{ xs: 12, md: 4 }}>
          <Card variant="outlined">
            <CardContent>
              <Typography variant="overline" color="text.secondary">Products you can reach</Typography>
              <Typography variant="h6">{products.length}</Typography>
              <Typography color="text.secondary">Viewing: {selected}</Typography>
            </CardContent>
          </Card>
        </Grid>
        <Grid size={{ xs: 12, md: 4 }}>
          <Card variant="outlined">
            <CardContent>
              <Typography variant="overline" color="text.secondary">Permissions</Typography>
              <Typography variant="h6">{me?.permissions.length ?? 0}</Typography>
              <Typography color="text.secondary">Hover your name above to see them</Typography>
            </CardContent>
          </Card>
        </Grid>
      </Grid>
    </>
  )
}
