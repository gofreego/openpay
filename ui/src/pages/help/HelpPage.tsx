import { Link as RouterLink, useParams } from 'react-router-dom'
import { Box, Button, Link, List, ListItemButton, ListItemText, Paper, useTheme } from '@mui/material'
import ArrowBackIcon from '@mui/icons-material/ArrowBack'
import { NotFoundPage, ReadmeViewer } from '@gofreego/tsutils'
import { PageHeader } from '../../components'
import { GUIDES, guideMarkdown } from '../../help/guides'

/** HelpPage shows one page's guide: what it is for, with screenshots. */
export function HelpPage() {
  const { slug = '' } = useParams()
  const theme = useTheme()
  const guide = GUIDES.find((g) => g.slug === slug)
  const markdown = guideMarkdown(slug)
  if (!guide || markdown === undefined) return <NotFoundPage />

  return (
    <>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', mb: 2 }}>
        <Button component={RouterLink} to={guide.path} startIcon={<ArrowBackIcon />}>Back to {guide.title}</Button>
        <Button component={RouterLink} to="/help">All guides</Button>
      </Box>
      <Paper variant="outlined" sx={{ p: { xs: 2, md: 4 }, maxWidth: 1100, '& img': { maxWidth: '100%', border: 1, borderColor: 'divider', borderRadius: 1 } }}>
        <ReadmeViewer content={markdown} muiTheme={theme} />
      </Paper>
    </>
  )
}

/** HelpIndex lists every guide. */
export function HelpIndex() {
  return (
    <>
      <PageHeader title="Guides" subtitle="How each page of the console works, with screenshots." />
      <Paper variant="outlined" sx={{ maxWidth: 640 }}>
        <List disablePadding>
          {GUIDES.map((g) => (
            <ListItemButton key={g.slug} component={RouterLink} to={`/help/${g.slug}`}>
              <ListItemText primary={g.title} secondary={<Link component="span" underline="none" color="text.secondary">{g.path}</Link>} />
            </ListItemButton>
          ))}
        </List>
      </Paper>
    </>
  )
}
