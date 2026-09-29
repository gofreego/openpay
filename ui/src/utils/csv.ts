/**
 * toCsv renders rows for a client-side export of what a table shows. Cells a
 * spreadsheet would run as formulas are neutralised, as the server does.
 */
export function toCsv(header: string[], rows: string[][]): string {
  const cell = (v: string) => {
    const safe = /^[=+\-@\t\r]/.test(v) ? `'${v}` : v
    return /[",\n]/.test(safe) ? `"${safe.replace(/"/g, '""')}"` : safe
  }
  return [header, ...rows].map((r) => r.map(cell).join(',')).join('\n')
}

export function saveText(name: string, text: string, type = 'text/csv;charset=utf-8'): void {
  const link = document.createElement('a')
  link.href = URL.createObjectURL(new Blob([text], { type }))
  link.download = name
  link.click()
  URL.revokeObjectURL(link.href)
}
