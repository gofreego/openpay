import { useState } from 'react'
import { Box, Button, Card, CardContent, Typography } from '@mui/material'
import PaletteIcon from '@mui/icons-material/Palette'
import LogoutIcon from '@mui/icons-material/Logout'
import { ThemeToggle } from '@gofreego/tsutils'
import { PageHeader } from '../../components'
import { logout } from '../../services/auth'

// Same layout as the other admin apps' settings (catalog).
export function SettingsPage() {
  const [loggingOut, setLoggingOut] = useState(false)

  const handleLogout = async () => {
    setLoggingOut(true)
    try {
      await logout()
    } finally {
      setLoggingOut(false)
    }
  }

  return (
    <>
      <PageHeader title="Settings" help="settings" subtitle="Manage your preferences and customize your workspace." />

      <Box sx={{ mt: 4 }}>
        <Typography variant="h5" fontWeight="600" sx={{ mb: 3, display: 'flex', alignItems: 'center', gap: 1.5 }}>
          <PaletteIcon color="primary" />
          Appearance
        </Typography>
        <Card
          elevation={0}
          sx={{
            borderRadius: 4,
            border: '1px solid',
            borderColor: 'divider',
            background: (theme) => `linear-gradient(135deg, ${theme.palette.background.paper} 0%, ${theme.palette.action.hover} 100%)`,
            transition: 'all 0.3s ease',
            '&:hover': {
              transform: 'translateY(-4px)',
              boxShadow: (theme) => theme.shadows[4],
            },
          }}
        >
          <CardContent sx={{ p: 4, '&:last-child': { pb: 4 } }}>
            <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <Box>
                <Typography variant="h6" fontWeight="600" gutterBottom>Theme Mode</Typography>
                <Typography variant="body2" color="text.secondary">
                  Personalize your experience by switching between light and dark modes.
                </Typography>
              </Box>
              <Box
                sx={{
                  p: 1.5,
                  borderRadius: 3,
                  bgcolor: 'background.paper',
                  boxShadow: (theme) => theme.shadows[1],
                  display: 'flex',
                  alignItems: 'center',
                }}
              >
                <ThemeToggle />
              </Box>
            </Box>
          </CardContent>
        </Card>
      </Box>

      <Box sx={{ mt: 4 }}>
        <Typography variant="h5" fontWeight="600" sx={{ mb: 3, display: 'flex', alignItems: 'center', gap: 1.5 }}>
          <LogoutIcon color="primary" />
          Account
        </Typography>
        <Card elevation={0} sx={{ borderRadius: 4, border: '1px solid', borderColor: 'divider' }}>
          <CardContent sx={{ p: 4, '&:last-child': { pb: 4 } }}>
            <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 2, flexWrap: 'wrap' }}>
              <Box>
                <Typography variant="h6" fontWeight="600" gutterBottom>Sign out</Typography>
                <Typography variant="body2" color="text.secondary">
                  End your current session and return to the login page.
                </Typography>
              </Box>
              <Button
                variant="outlined"
                color="error"
                startIcon={<LogoutIcon />}
                onClick={() => void handleLogout()}
                disabled={loggingOut}
                sx={{ textTransform: 'none', borderRadius: 2 }}
              >
                {loggingOut ? 'Logging out...' : 'Logout'}
              </Button>
            </Box>
          </CardContent>
        </Card>
      </Box>
    </>
  )
}
