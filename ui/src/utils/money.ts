// Money in the console (plan.md U-D4).
//
// Every amount from the API is an int64 in minor units, sent as a JSON
// string. It stays a string or a BigInt from the wire to the screen: a
// JavaScript number is a float64, which cannot hold every int64 and cannot
// add 0.1 and 0.2. Nothing else in the console may format or do arithmetic
// on an amount — ESLint forbids parseFloat, Number() and toFixed to make that
// hold.

/** Minor units per major unit is 10^exponent. */
const EXPONENTS: Record<string, number> = { INR: 2, USD: 2, EUR: 2, JPY: 0 }

export function exponent(currency: string): number {
  return EXPONENTS[currency.toUpperCase()] ?? 2
}

const SYMBOLS: Record<string, string> = { INR: '₹', USD: '$', EUR: '€', JPY: '¥' }

/** parse reads an API amount (minor units, as a string). */
export function parse(minor: string | bigint | undefined | null): bigint {
  if (minor === undefined || minor === null || minor === '') return 0n
  if (typeof minor === 'bigint') return minor
  if (!/^-?\d+$/.test(minor.trim())) {
    throw new Error(`not an amount in minor units: ${JSON.stringify(minor)}`)
  }
  return BigInt(minor.trim())
}

export function add(...amounts: (string | bigint)[]): bigint {
  return amounts.reduce<bigint>((sum, a) => sum + parse(a), 0n)
}

export function sub(a: string | bigint, b: string | bigint): bigint {
  return parse(a) - parse(b)
}

/**
 * allocate splits an amount by ratios without losing or inventing a paisa:
 * the parts always sum to the total, and leftover units go to the largest
 * remainders, earliest first on ties — the same rule as the server's
 * money.Allocate, so a preview matches what gets posted.
 */
export function allocate(total: string | bigint, ratios: number[]): bigint[] {
  const amount = parse(total)
  if (ratios.length === 0 || ratios.some((r) => !Number.isInteger(r) || r < 0)) {
    throw new Error('ratios must be non-negative integers')
  }
  const sum = ratios.reduce((s, r) => s + BigInt(r), 0n)
  if (sum === 0n) throw new Error('ratios must not all be zero')

  const negative = amount < 0n
  const abs = negative ? -amount : amount
  const parts = ratios.map((r) => (abs * BigInt(r)) / sum)
  const remainders = ratios.map((r, i) => ({ i, rem: (abs * BigInt(r)) % sum }))
  let left = abs - parts.reduce((s, p) => s + p, 0n)
  remainders.sort((a, b) => (a.rem === b.rem ? a.i - b.i : a.rem > b.rem ? -1 : 1))
  for (const { i } of remainders) {
    if (left === 0n) break
    parts[i] += 1n
    left -= 1n
  }
  return negative ? parts.map((p) => -p) : parts
}

/**
 * toMajor renders minor units as a plain decimal, e.g. "-1234.50". For
 * inputs and exports; use format for display.
 */
export function toMajor(minor: string | bigint, currency: string): string {
  const value = parse(minor)
  const e = exponent(currency)
  const negative = value < 0n
  const abs = negative ? -value : value
  if (e === 0) return `${negative ? '-' : ''}${abs}`
  const div = 10n ** BigInt(e)
  const whole = abs / div
  const frac = (abs % div).toString().padStart(e, '0')
  return `${negative ? '-' : ''}${whole}.${frac}`
}

/**
 * fromMajor reads what a person typed ("1,234.5", "₹ 1234.50") into minor
 * units. It refuses more decimals than the currency has rather than
 * rounding: a person who typed ₹10.005 meant something, and it was not ₹10.01.
 */
export function fromMajor(input: string, currency: string): bigint {
  const cleaned = input.replace(/[\s,₹$€¥]/g, '')
  const e = exponent(currency)
  const match = /^(-?)(\d+)(?:\.(\d*))?$/.exec(cleaned)
  if (!match) throw new Error(`"${input}" is not an amount`)
  const [, sign, whole, frac = ''] = match
  if (frac.length > e) {
    throw new Error(`${currency} has ${e} decimal places; "${input}" has ${frac.length}`)
  }
  const minor = BigInt(whole) * 10n ** BigInt(e) + BigInt(frac.padEnd(e, '0') || '0')
  return sign === '-' ? -minor : minor
}

/**
 * format renders an amount for people: symbol, Indian digit grouping for
 * INR, always the currency's full decimals — "₹1,23,456.70", "-₹5.00".
 */
export function format(minor: string | bigint, currency: string): string {
  const major = toMajor(minor, currency)
  const negative = major.startsWith('-')
  const [whole, frac] = (negative ? major.slice(1) : major).split('.')
  const locale = currency.toUpperCase() === 'INR' ? 'en-IN' : 'en-US'
  // Intl formats a BigInt exactly; only the whole part goes through it.
  const grouped = new Intl.NumberFormat(locale).format(BigInt(whole))
  const symbol = SYMBOLS[currency.toUpperCase()] ?? `${currency.toUpperCase()} `
  return `${negative ? '-' : ''}${symbol}${grouped}${frac !== undefined ? `.${frac}` : ''}`
}

export const Money = { parse, add, sub, allocate, toMajor, fromMajor, format, exponent }
