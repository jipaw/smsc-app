import { useState } from 'react'
import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { get, post, put } from '../lib/api'
import { isAdmin, useMe } from '../lib/session'
import { useEventStream } from '../lib/sse'
import type { Overview } from '../lib/types'
import { errMsg, useToast } from './ui'

const NAV: [string, string][] = [
  ['/', 'Overview'], ['/devices', 'Devices'], ['/sims', 'SIM'], ['/send', 'Kirim SMS'], ['/transactions', 'Transaksi'],
  ['/campaigns', 'Campaigns'], ['/templates', 'Template'], ['/settings', 'Pengaturan'],
]

export function Layout({ onLogout }: { onLogout: () => void }) {
  const me = useMe()
  const sse = useEventStream()
  const [open, setOpen] = useState(false)
  const nav = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()
  const ov = useQuery({ queryKey: ['overview'], queryFn: () => get<Overview>('/stats/overview'), refetchInterval: 30000 })
  const resume = useMutation({
    mutationFn: () => put('/settings/dispatch-pause', { paused: false }),
    onSuccess: () => { qc.invalidateQueries(); toast('Pengiriman dilanjutkan') }, onError: (e) => toast(errMsg(e), true),
  })
  const logout = async () => { await post('/auth/logout'); onLogout(); nav('/') }
  const sseLabel = { live: 'Realtime tersambung', connecting: 'Menyambung…', down: 'Realtime terputus' }[sse]

  return (
    <div className="shell">
      <aside className={`side ${open ? 'open' : ''}`}>
        <div className="brand">SMSC Gateway</div>
        <nav aria-label="Navigasi utama">
          {NAV.map(([to, label]) => <NavLink key={to} to={to} end={to === '/'} onClick={() => setOpen(false)}>{label}</NavLink>)}
        </nav>
      </aside>
      <div className="main">
        {ov.data?.paused && (
          <div className="banner" role="alert">
            ⏸ Semua pengiriman DIJEDA (kill switch aktif)
            {isAdmin(me) && <button onClick={() => resume.mutate()} disabled={resume.isPending}>Lanjutkan</button>}
          </div>
        )}
        <header className="top">
          <div className="row">
            <button className="menu-btn small" onClick={() => setOpen((v) => !v)} aria-label="Menu">☰</button>
            <span className="badge muted" title={sseLabel}><span aria-hidden="true">{sse === 'live' ? '●' : '○'}</span>{sseLabel}</span>
          </div>
          <div className="row">
            <span className="muted">{me.email} · {me.role}</span>
            <button className="small" onClick={logout}>Keluar</button>
          </div>
        </header>
        <main className="content"><Outlet /></main>
      </div>
    </div>
  )
}
