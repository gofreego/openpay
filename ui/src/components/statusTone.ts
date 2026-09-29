export type Tone = 'success' | 'warning' | 'error' | 'info' | 'default'

// One table for every state machine in OpenPay (plan.md D6), so a status
// looks the same on every screen. Keys are the plain state name: enum
// prefixes ("PAYMENT_STATUS_") are stripped before lookup.
const TONES: Record<string, Tone> = {
  // Done, and good.
  captured: 'success', settled: 'success', paid: 'success', won: 'success', verified: 'success',
  applied: 'success', active: 'success', resolved: 'success', matched: 'success', succeeded: 'success',
  refunded: 'success', completed: 'success', passed: 'success', healthy: 'success', force_matched: 'success',
  // In flight: someone or something is still deciding.
  created: 'info', pending: 'info', authorized: 'info', processing: 'info', initiated: 'info',
  approved: 'info', running: 'info', pending_payment: 'info',
  // Needs a person.
  pending_approval: 'warning', open: 'warning', under_review: 'warning', unapplied: 'warning',
  suspense: 'warning', partially_refunded: 'warning', disabled: 'warning', forced: 'warning',
  // Went wrong, or money went the wrong way.
  failed: 'error', rejected: 'error', lost: 'error', reversed: 'error', written_off: 'error',
  frozen: 'error', suspended: 'error', unhealthy: 'error', drift: 'error',
  // Ended quietly.
  expired: 'default', cancelled: 'default', closed: 'default', archived: 'default', released: 'default',
  unspecified: 'default',
}

/** stateName turns "PAYMENT_STATUS_PENDING_APPROVAL" or "pending_approval" into "pending_approval". */
export function stateName(status: string): string {
  const lower = status.toLowerCase()
  const match = /^[a-z]+(?:_[a-z]+)*?_status_(.+)$/.exec(lower)
  return match ? match[1] : lower
}

export function toneOf(status: string): Tone {
  return TONES[stateName(status)] ?? 'default'
}

export function labelOf(status: string): string {
  const name = stateName(status).replace(/_/g, ' ')
  return name.charAt(0).toUpperCase() + name.slice(1)
}
