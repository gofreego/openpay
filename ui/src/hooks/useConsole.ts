import { useContext } from 'react'
import { ConsoleContext, type ConsoleState } from './consoleState'
import type { Permissions } from './permissions'

export function useConsole(): ConsoleState {
  const state = useContext(ConsoleContext)
  if (!state) throw new Error('useConsole must be used inside ConsoleProvider')
  return state
}

/** usePermissions gates nav items and actions; the server still decides. */
export function usePermissions(): Permissions {
  return useConsole().permissions
}
