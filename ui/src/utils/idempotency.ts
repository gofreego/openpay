/**
 * newIdempotencyKey makes a key for one operator action. Random, so two
 * operators — or two tabs — never collide; held by the caller for the life
 * of the action so retries reuse it.
 */
export function newIdempotencyKey(): string {
  return `console-${crypto.randomUUID()}`
}
