import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { get, post } from '../lib/api'
import { bellLabel, notificationTarget, notificationText } from '../lib/notify'
import { fmtTime } from '../lib/format'
import type { Notification } from '../lib/types'

/** Lonceng notifikasi di header: jumlah belum dibaca, daftar, tandai dibaca. Disegarkan oleh event SSE notification.created. */
export function NotificationBell() {
  const qc = useQueryClient()
  const nav = useNavigate()
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const q = useQuery({ queryKey: ['notifications'], queryFn: () => get<{ items: Notification[]; unread_count: number }>('/notifications?limit=20'), refetchInterval: 60000 })
  const items = q.data?.items ?? []
  const unread = q.data?.unread_count ?? 0
  const read = useMutation({ mutationFn: (id: string) => post(`/notifications/${id}/read`), onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications'] }) })
  const readAll = useMutation({ mutationFn: () => post('/notifications/read-all'), onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications'] }) })
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    const onDown = (e: MouseEvent) => { if (box.current && !box.current.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('keydown', onKey); document.addEventListener('mousedown', onDown)
    return () => { document.removeEventListener('keydown', onKey); document.removeEventListener('mousedown', onDown) }
  }, [open])
  const openItem = (n: Notification) => {
    if (!n.read_at) read.mutate(n.id)
    const to = notificationTarget(n)
    setOpen(false)
    if (to) nav(to)
  }
  return (
    <div ref={box} style={{ position: 'relative' }}>
      <button className="small" aria-label={bellLabel(unread)} aria-expanded={open} aria-haspopup="true" onClick={() => setOpen((v) => !v)}>
        <span aria-hidden="true">🔔</span>{unread > 0 && <span className="badge bad" aria-hidden="true" style={{ marginLeft: 4 }}>{unread > 99 ? '99+' : unread}</span>}
      </button>
      {open && (
        <div className="card stack" role="region" aria-label="Daftar notifikasi" style={{ position: 'absolute', right: 0, top: '110%', width: 340, maxWidth: '90vw', zIndex: 20 }}>
          <div className="row spread"><strong>Notifikasi</strong><button className="small" disabled={unread === 0 || readAll.isPending} onClick={() => readAll.mutate()}>Tandai semua dibaca</button></div>
          {items.length === 0 && <p className="muted">Belum ada notifikasi.</p>}
          <ul style={{ listStyle: 'none', margin: 0, padding: 0 }} className="stack">
            {items.map((n) => (
              <li key={n.id}>
                <button className="small" style={{ width: '100%', textAlign: 'left', fontWeight: n.read_at ? 400 : 700 }} onClick={() => openItem(n)}>
                  {!n.read_at && <span className="sr-only">Belum dibaca: </span>}{notificationText(n)}
                  <div className="hint">{fmtTime(n.created_at)}</div>
                </button>
              </li>))}
          </ul>
        </div>)}
    </div>
  )
}
