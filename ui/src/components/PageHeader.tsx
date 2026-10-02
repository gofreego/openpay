import { Link as RouterLink } from 'react-router-dom'
import { Box, IconButton, Tooltip, Typography } from '@mui/material'
import InfoOutlinedIcon from '@mui/icons-material/InfoOutlined'
import type { GuideSlug } from '../help/guides'

interface PageHeaderProps {
  title: React.ReactNode
  subtitle?: string | React.ReactNode
  action?: React.ReactNode
  /** The page's guide (src/help/<slug>.md), opened by an info button next to the title. */
  help?: GuideSlug
}

/**
 * Standardized Page Header component used across all admin pages.
 */
export const PageHeader = ({ title, subtitle, action, help }: PageHeaderProps) => {
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
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
          <Typography 
            variant="h4" 
            fontWeight="700" 
            color="primary.main"
          >
            {title}
          </Typography>
          {help && (
            <Tooltip title="How this page works">
              <IconButton component={RouterLink} to={`/help/${help}`} size="small" aria-label="How this page works" color="primary">
                <InfoOutlinedIcon />
              </IconButton>
            </Tooltip>
          )}
        </Box>
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
