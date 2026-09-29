import { Box, Typography } from '@mui/material'

interface PageHeaderProps {
  title: string
  subtitle?: string | React.ReactNode
  action?: React.ReactNode
}

/**
 * Standardized Page Header component used across all admin pages.
 */
export const PageHeader = ({ title, subtitle, action }: PageHeaderProps) => {
  return (
    <Box sx={{ mb: 4 }}>
      <Box 
        sx={{ 
          display: 'flex', 
          justifyContent: 'space-between', 
          alignItems: 'center',
          gap: 2,
          mb: subtitle ? 0.5 : 0 
        }}
      >
        <Typography 
          variant="h4" 
          fontWeight="700" 
          color="primary.main"
        >
          {title}
        </Typography>
        {action && <Box sx={{ flexShrink: 0 }}>{action}</Box>}
      </Box>
      {subtitle && (
        <Typography variant="body1" color="text.secondary">
          {subtitle}
        </Typography>
      )}
    </Box>
  )
}
