import { createContext, useContext } from 'react'
import type { Me } from './types'

export const MeContext = createContext<Me | null>(null)
export const useMe = () => {
  const m = useContext(MeContext)
  if (!m) throw new Error('belum login')
  return m
}
export const MeRefreshContext = createContext<() => Promise<void>>(async () => {})
export const useRefreshMe = () => useContext(MeRefreshContext)
export const isAdmin = (m: Me) => m.role === 'admin'
export const canOperate = (m: Me) => m.role === 'admin' || m.role === 'operator'
