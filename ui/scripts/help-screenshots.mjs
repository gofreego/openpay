// Captures the screenshots the page guides (src/help/*.md) show, from a
// running console: `npm run dev` against a local OpenPay with the dev
// operator headers set (.env.development.local), then
//
//   npm run help:screenshots
//
// It drives your installed Chrome (CHROME_PATH overrides the macOS default)
// and writes public/help/<name>.png. Detail pages open the first record the
// API returns, so the local database needs some data.
import { mkdir } from 'node:fs/promises'
import puppeteer from 'puppeteer-core'
import { loadEnv } from 'vite'

const env = loadEnv('development', process.cwd(), '')
const CONSOLE = process.env.CONSOLE_URL ?? 'http://localhost:5173/payments'
const API = `${env.DEV_OPENPAY_URL ?? 'http://localhost:8085'}/openpay/v1`
const CHROME = process.env.CHROME_PATH ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const OUT = 'public/help'

const headers = { 'x-user-id': env.VITE_DEV_USER_ID ?? 'dev-operator', 'x-user-perms': env.VITE_DEV_USER_PERMS ?? '' }

/** first returns the first record of a list endpoint, or undefined. */
async function first(path, key) {
  const res = await fetch(`${API}${path}`, { headers })
  if (!res.ok) return undefined
  return (await res.json())[key]?.[0]
}

// A settled payment tells the normal story; a stuck one fills its timeline
// with polls.
const payment = await first('/payments?limit=1&status=PAYMENT_STATUS_SETTLED', 'payments')
  ?? await first('/payments?limit=1&status=PAYMENT_STATUS_CAPTURED', 'payments')
  ?? await first('/payments?limit=1', 'payments')
const order = await first('/orders?limit=1', 'orders')
const dispute = await first('/disputes?limit=1', 'disputes')
const withdrawal = await first('/withdrawals?limit=1', 'withdrawals')
const product = await first('/products?limit=1', 'products')
const account = await first('/ledger/accounts?limit=1&code_prefix=wallet', 'accounts') ?? await first('/ledger/accounts?limit=1', 'accounts')
const walletType = product && await first(`/wallet-types?product_id=${product.id}`, 'walletTypes')

// [file, console path, whole page?]. Lists are cut at the window; detail
// pages are shot whole because their sections are the point.
const shots = [
  ['dashboard', '/dashboard'],
  ['customers', '/customers'],
  payment && ['customer-detail', `/customers/${payment.customerId}`, true],
  payment?.walletId && ['wallet-detail', `/wallets/${payment.walletId}`, true],
  ['payments', '/payments'],
  payment && ['payment-detail', `/payments/${payment.id}`, true],
  ['orders', '/orders'],
  order && ['order-detail', `/orders/${order.id}`, true],
  ['refunds', '/refunds'],
  ['disputes', '/disputes'],
  dispute && ['dispute-detail', `/disputes/${dispute.id}`, true],
  ['withdrawals', '/withdrawals'],
  withdrawal && ['withdrawal-detail', `/withdrawals/${withdrawal.id}`, true],
  ['ledger-accounts', '/ledger/accounts'],
  account && ['ledger-account-detail', `/ledger/accounts/${account.id}`],
  ['trial-balance', '/ledger/trial-balance'],
  ['ledger-checks', '/ledger/checks'],
  ['recon', '/recon'],
  ['providers', '/providers'],
  ['reports', '/reports'],
  ['products', '/products'],
  product && ['product-detail', `/products/${product.id}`, true],
  ['wallet-types-pick', '/wallet-types'],
  product && ['wallet-types', `/wallet-types?product=${product.id}`],
  walletType && ['wallet-type-detail', `/wallet-types/${walletType.id}`, true],
  ['audit', '/audit'],
  ['settings', '/settings'],
].filter(Boolean)

await mkdir(OUT, { recursive: true })
const browser = await puppeteer.launch({ executablePath: CHROME, headless: true, defaultViewport: { width: 1440, height: 900 } })
try {
  const page = await browser.newPage()
  for (const [name, path, fullPage] of shots) {
    await page.setViewport({ width: 1440, height: 900 })
    await page.goto(`${CONSOLE}${path}`, { waitUntil: 'networkidle2', timeout: 30000 })
    // Let chips and late requests settle.
    await new Promise((r) => setTimeout(r, 1200))
    if (fullPage) {
      // The layout is 100vh and scrolls inside, so the page itself never
      // grows; make the window as tall as the tallest scrolling content.
      const height = await page.evaluate(() => Math.max(...[...document.querySelectorAll('*')].map((el) => el.scrollHeight)))
      await page.setViewport({ width: 1440, height: Math.min(Math.max(height, 900), 2400) })
      await new Promise((r) => setTimeout(r, 400))
    }
    await page.screenshot({ path: `${OUT}/${name}.png` })
    console.log(`${OUT}/${name}.png`)
  }
} finally {
  await browser.close()
}
