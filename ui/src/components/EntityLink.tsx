import { Link } from '@mui/material'
import { Link as RouterLink } from 'react-router-dom'
import { routeFor } from '../utils/routes'

/** EntityLink shows a public id, linked to its page when it has one. */
export function EntityLink({ id, label }: { id: string | undefined; label?: string }) {
  if (!id) return <>—</>
  const to = routeFor(id)
  const text = label ?? id
  const style = { fontFamily: label ? undefined : 'monospace', fontSize: label ? undefined : 13 }
  return to
    ? <Link component={RouterLink} to={to} onClick={(e) => e.stopPropagation()} sx={style}>{text}</Link>
    : <span style={style}>{text}</span>
}
