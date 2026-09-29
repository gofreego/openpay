import type { ReactNode } from 'react'
import { Box, Paper, Typography } from '@mui/material'

/** KeyValue lays out a detail page's facts as label/value pairs. */
export function KeyValue({ items, title }: { items: [string, ReactNode][]; title?: string }) {
  return (
    <Paper variant="outlined" sx={{ p: 2 }}>
      {title && <Typography variant="subtitle1" fontWeight={600} sx={{ mb: 1 }}>{title}</Typography>}
      <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: '200px 1fr' }, rowGap: 1, columnGap: 2 }}>
        {items.map(([label, value]) => (
          <Box key={label} sx={{ display: 'contents' }}>
            <Typography color="text.secondary" variant="body2">{label}</Typography>
            <Box sx={{ wordBreak: 'break-all' }}>{value === '' || value === undefined || value === null ? '—' : value}</Box>
          </Box>
        ))}
      </Box>
    </Paper>
  )
}

/** Section is a titled block on a detail page. */
export function Section({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return (
    <Box sx={{ mt: 3 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', mb: 1 }}>
        <Typography variant="h6" sx={{ flex: 1 }}>{title}</Typography>
        {action}
      </Box>
      {children}
    </Box>
  )
}
