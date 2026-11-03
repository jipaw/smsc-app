import { FormEvent, useEffect, useState } from 'react'
import { ApiError, post } from '../lib/api'
import { ErrorBox } from '../components/ui'
import { fmtTime } from '../lib/format'

type Info = { email: string; role: string; expires_at: string }

/** Halaman publik /invite/<token>: penerima menetapkan passwordnya sendiri. */
export function Invite({ token }: { token: string }) {
  const [info, setInfo] = useState<Info | null | undefined>(undefined)
  const [pw, setPw] = useState('')
  const [again, setAgain] = useState('')
  const [err, setErr] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState(false)
  useEffect(() => {
    post<Info>('/invitations/lookup', { token }).then(setInfo).catch((e) => setInfo(e instanceof ApiError && e.status === 404 ? null : null))
  }, [token])
  const mismatch = again !== '' && again !== pw
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (mismatch) return
    setBusy(true); setErr(null)
    try { await post('/invitations/accept', { token, password: pw }); setDone(true) } catch (x) { setErr(x) } finally { setBusy(false) }
  }
  return (
    <div className="login">
      <div className="card stack">
        <h1>SMSC Gateway</h1>
        {info === undefined && <p className="muted">Memeriksa undangan…</p>}
        {info === null && !done && <div className="err" role="alert">Undangan tidak valid: tidak ditemukan, sudah dipakai, dicabut, atau kedaluwarsa. Minta admin mengirim undangan baru.</div>}
        {done && (
          <div className="stack"><div className="okbox" role="status">Akun dibuat. Silakan masuk dengan email dan password yang baru Anda tetapkan.</div>
            <a className="btn primary" style={{ textDecoration: 'none', textAlign: 'center' }} href="/">Ke halaman masuk</a></div>
        )}
        {info && !done && (
          <form className="stack" onSubmit={submit}>
            <p>Anda diundang sebagai <strong>{info.role}</strong> dengan email <strong>{info.email}</strong>. <span className="muted">Berlaku sampai {fmtTime(info.expires_at)}.</span></p>
            <label className="field"><span>Password baru <span className="muted">(min. 12 karakter)</span></span><input type="password" autoComplete="new-password" minLength={12} value={pw} onChange={(e) => setPw(e.target.value)} required autoFocus /></label>
            <label className="field"><span>Ulangi password</span><input type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} required /></label>
            {mismatch && <div className="err" role="alert">Password tidak sama.</div>}
            <ErrorBox error={err} />
            <button className="primary" disabled={busy || mismatch || pw.length < 12}>{busy ? 'Membuat akun…' : 'Buat akun'}</button>
          </form>
        )}
      </div>
    </div>
  )
}
