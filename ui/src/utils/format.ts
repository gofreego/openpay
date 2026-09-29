/** dateTime renders an API timestamp in the operator's locale, or "—". */
export function dateTime(value: string | undefined | null): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return '—'
  return d.toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'medium' })
}

/** isoDaysAgo is an RFC 3339 timestamp n days before now, for period filters. */
export function isoDaysAgo(days: number): string {
  return new Date(Date.now() - days * 86_400_000).toISOString()
}

/** enumLabel turns "PAYMENT_STATUS_PENDING" into "Pending". */
export function enumLabel(value: string, prefix: string): string {
  const name = value.replace(prefix, '').toLowerCase().replace(/_/g, ' ')
  return name.charAt(0).toUpperCase() + name.slice(1)
}

/** pretty re-indents a JSON string for reading; returns it unchanged if it is not JSON. */
export function prettyJson(raw: string): string {
  if (!raw) return ''
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}
