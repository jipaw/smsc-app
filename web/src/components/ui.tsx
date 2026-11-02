import { createContext, ReactNode, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { ApiError } from '../lib/api'
import { STATUS_LABEL } from '../lib/format'

export function Badge({ status, label }: { status: string; label?: string }) {
  const s = STATUS_LABEL[status] ?? { label: status, icon: '•', tone: 'muted' }
  return <span className={`badge ${s.tone}`}><span aria-hidden="true">{s.icon}</span>{label ?? s.label}</span>
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null
  const msg = error instanceof ApiError ? error.pretty : error instanceof Error ? error.message : String(error)
  return <div className="err" role="alert">{msg}</div>
}

export function Loading({ what = 'data' }: { what?: string }) {
  return <p className="muted" role="status">Memuat {what}…</p>
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="card muted" style={{ textAlign: 'center', padding: 28 }}>{children}</div>
}

/** Modal memakai elemen <dialog> native: fokus terkunci, Esc menutup, backdrop. */
export function Modal({ open, onClose, title, children, wide, drawer }: {
  open: boolean; onClose: () => void; title: string; children: ReactNode; wide?: boolean; drawer?: boolean
}) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const d = ref.current
    if (!d) return
    if (open && !d.open) d.showModal()
    if (!open && d.open) d.close()
  }, [open])
  return (
    <dialog ref={ref} className={`${wide ? 'wide' : ''} ${drawer ? 'drawer' : ''}`} aria-label={title}
      onClose={onClose} onClick={(e) => { if (e.target === ref.current) onClose() }}>
      {open && (
        <div className="dlg">
          <div className="row spread"><h2>{title}</h2><button className="small" onClick={onClose} aria-label="Tutup">✕</button></div>
          {children}
        </div>
      )}
    </dialog>
  )
}

type Toast = { id: number; text: string; bad?: boolean }
const ToastCtx = createContext<(text: string, bad?: boolean) => void>(() => {})
export const useToast = () => useContext(ToastCtx)

export function ToastHost({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Toast[]>([])
  const push = useCallback((text: string, bad?: boolean) => {
    const id = Date.now() + Math.random()
    setItems((s) => [...s, { id, text, bad }])
    window.setTimeout(() => setItems((s) => s.filter((t) => t.id !== id)), 4500)
  }, [])
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="toasts" role="status" aria-live="polite">{items.map((t) => <div key={t.id} className={`toast ${t.bad ? 'bad' : ''}`}>{t.text}</div>)}</div>
    </ToastCtx.Provider>
  )
}

export function errMsg(e: unknown): string {
  return e instanceof ApiError ? e.pretty : e instanceof Error ? e.message : String(e)
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return <label className="field"><span>{label}</span>{children}{hint && <small className="hint">{hint}</small>}</label>
}

export function Confirm({ open, title, text, confirmWord, onConfirm, onClose, danger = true }: {
  open: boolean; title: string; text: ReactNode; confirmWord?: string; onConfirm: () => void; onClose: () => void; danger?: boolean
}) {
  const [typed, setTyped] = useState('')
  useEffect(() => { if (!open) setTyped('') }, [open])
  const ok = !confirmWord || typed === confirmWord
  return (
    <Modal open={open} onClose={onClose} title={title}>
      <div>{text}</div>
      {confirmWord && <Field label={`Ketik "${confirmWord}" untuk melanjutkan`}><input value={typed} onChange={(e) => setTyped(e.target.value)} autoFocus /></Field>}
      <div className="row" style={{ justifyContent: 'flex-end' }}>
        <button onClick={onClose}>Batal</button>
        <button className={danger ? 'danger' : 'primary'} disabled={!ok} onClick={onConfirm}>Lanjutkan</button>
      </div>
    </Modal>
  )
}
