import { useState } from 'react'
import { Box, Button, Collapse, Typography } from '@mui/material'
import type { AuditEntry } from '../apis/proto/openpay/v1/audit'
import { auditService } from '../services'
import { useAsync, usePermissions, Perm } from '../hooks'
import { dateTime } from '../utils/format'
import { DataTable } from './DataTable'
import { JsonView } from './JsonView'

/**
 * AuditPanel shows who changed a resource, when, and what changed — embedded
 * on each detail page. Hidden without openpay:audit:read.
 */
export function AuditPanel({ resourceType, resourceId }: { resourceType: string; resourceId: string }) {
  const permissions = usePermissions()
  const [open, setOpen] = useState<string | null>(null)
  const audit = useAsync(() => auditService.list({ resourceType, resourceId, limit: 50 }), [resourceType, resourceId])
  if (!permissions.can(Perm.auditRead)) return null

  return (
    <Box sx={{ mt: 3 }}>
      <Typography variant="h6" sx={{ mb: 1 }}>History</Typography>
      <DataTable<AuditEntry>
        rows={audit.data?.entries ?? []}
        rowKey={(e) => e.id}
        loading={audit.loading}
        error={audit.error}
        emptyText="No recorded changes"
        columns={[
          { key: 'when', header: 'When', render: (e) => dateTime(e.createdAt) },
          { key: 'who', header: 'Who', render: (e) => `${e.actorId} (${e.actorType})` },
          { key: 'what', header: 'What', render: (e) => e.action },
          {
            key: 'detail', header: '', align: 'right', render: (e) => (
              <Box>
                <Button size="small" onClick={() => setOpen(open === e.id ? null : e.id)}>{open === e.id ? 'Hide' : 'Changes'}</Button>
                <Collapse in={open === e.id} unmountOnExit>
                  <Box sx={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 1, textAlign: 'left' }}>
                    <Box><Typography variant="caption">Before</Typography><JsonView raw={e.before || '{}'} /></Box>
                    <Box><Typography variant="caption">After</Typography><JsonView raw={e.after || '{}'} /></Box>
                  </Box>
                </Collapse>
              </Box>
            ),
          },
        ]}
      />
    </Box>
  )
}
