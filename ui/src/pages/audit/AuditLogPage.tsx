import { useState } from 'react'
import { Box, TextField, Typography } from '@mui/material'
import type { AuditEntry } from '../../apis/proto/openpay/v1/audit'
import { DataTable, EntityLink, JsonView, PageHeader } from '../../components'
import { useAsync } from '../../hooks'
import { auditService } from '../../services'
import { dateTime } from '../../utils/format'

export function AuditLogPage() {
  const [actorId, setActorId] = useState('')
  const [resourceId, setResourceId] = useState('')
  const [stack, setStack] = useState<string[]>([])
  const [cursor, setCursor] = useState('')
  const [open, setOpen] = useState<string | null>(null)
  const page = useAsync(() => auditService.list({ actorId: actorId || undefined, resourceId: resourceId || undefined, limit: 50, cursor: cursor || undefined }),
    [actorId, resourceId, cursor])
  const reset = () => { setStack([]); setCursor('') }

  return (
    <>
      <PageHeader title="Audit log" subtitle="Every recorded change: who, what, and the state before and after." />
      <DataTable<AuditEntry>
        rows={page.data?.entries ?? []}
        rowKey={(e) => e.id}
        loading={page.loading}
        error={page.error}
        onRowClick={(e) => setOpen(open === e.id ? null : e.id)}
        filters={<>
          <TextField size="small" label="Who (user or credential)" value={actorId} onChange={(e) => { setActorId(e.target.value.trim()); reset() }} />
          <TextField size="small" label="Resource id" value={resourceId} onChange={(e) => { setResourceId(e.target.value.trim()); reset() }} />
        </>}
        pagination={{
          mode: 'cursor', hasPrevious: stack.length > 0, hasNext: !!page.data?.nextCursor,
          onPrevious: () => { setCursor(stack[stack.length - 1]); setStack(stack.slice(0, -1)) },
          onNext: () => { setStack([...stack, cursor]); setCursor(page.data?.nextCursor ?? '') },
        }}
        columns={[
          { key: 'when', header: 'When', render: (e) => dateTime(e.createdAt) },
          { key: 'who', header: 'Who', render: (e) => `${e.actorId} (${e.actorType})` },
          { key: 'what', header: 'What', render: (e) => (
            <Box>
              {e.action}
              {open === e.id && (
                <Box sx={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 1, mt: 1 }}>
                  <Box><Typography variant="caption">Before</Typography><JsonView raw={e.before || '{}'} /></Box>
                  <Box><Typography variant="caption">After</Typography><JsonView raw={e.after || '{}'} /></Box>
                </Box>
              )}
            </Box>
          ) },
          { key: 'resource', header: 'Resource', render: (e) => <EntityLink id={e.resourceId} /> },
          { key: 'request', header: 'Request', render: (e) => <code>{e.requestId}</code> },
        ]}
      />
    </>
  )
}
