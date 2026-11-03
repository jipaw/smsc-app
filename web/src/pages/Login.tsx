import { FormEvent, useState } from 'react'
import { post, setCsrf } from '../lib/api'
import type { Me, MfaChallenge } from '../lib/types'
import { ErrorBox } from '../components/ui'

export function Login({ onLogin }: { onLogin: (m: Me) => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [mfaToken, setMfaToken] = useState('')
  const [code, setCode] = useState('')
  const [err, setErr] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const done = (me: Me) => { setCsrf(me.csrf_token); onLogin(me) }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(null)
    try {
      const r = await post<Me | MfaChallenge>('/auth/login', { email, password })
      if ('mfa_required' in r) { setMfaToken(r.mfa_token); setPassword('') } else done(r)
    } catch (x) { setErr(x) } finally { setBusy(false) }
  }
  const verify = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(null)
    try { done(await post<Me>('/auth/login/verify', { mfa_token: mfaToken, code: code.trim() })) }
    catch (x) { setErr(x); setCode('') } finally { setBusy(false) }
  }
  if (mfaToken) {
    return (
      <div className="login">
        <form className="card stack" onSubmit={verify}>
          <h1>Verifikasi 2 langkah</h1>
          <p className="muted">Masukkan kode 6 digit dari aplikasi authenticator, atau satu kode pemulihan.</p>
          <label className="field"><span>Kode</span><input inputMode="text" autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} required autoFocus /></label>
          <ErrorBox error={err} />
          <button className="primary" disabled={busy || !code.trim()}>{busy ? 'Memeriksa…' : 'Verifikasi'}</button>
          <button type="button" className="small" onClick={() => { setMfaToken(''); setCode(''); setErr(null) }}>Kembali</button>
        </form>
      </div>
    )
  }
  return (
    <div className="login">
      <form className="card stack" onSubmit={submit}>
        <h1>SMSC Gateway</h1>
        <p className="muted">Masuk ke dashboard</p>
        <label className="field"><span>Email</span><input type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus /></label>
        <label className="field"><span>Password</span><input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required /></label>
        <ErrorBox error={err} />
        <button className="primary" disabled={busy}>{busy ? 'Masuk…' : 'Masuk'}</button>
      </form>
    </div>
  )
}
