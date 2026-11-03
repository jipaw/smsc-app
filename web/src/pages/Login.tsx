import { FormEvent, useEffect, useState } from 'react'
import { ApiError, get, post, setCsrf } from '../lib/api'
import type { Me, MfaChallenge } from '../lib/types'
import { getCredential, waErrMsg, webauthnSupported } from '../lib/webauthn'
import { ErrorBox } from '../components/ui'

export function Login({ onLogin }: { onLogin: (m: Me) => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [mfaToken, setMfaToken] = useState('')
  const [methods, setMethods] = useState<string[]>(['totp', 'recovery'])
  const [code, setCode] = useState('')
  const [err, setErr] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const [passkeyOk, setPasskeyOk] = useState(false)
  useEffect(() => { get<{ passkey_login: boolean }>('/auth/methods').then((m) => setPasskeyOk(m.passkey_login && webauthnSupported())).catch(() => {}) }, [])
  const done = (me: Me) => { setCsrf(me.csrf_token); onLogin(me) }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(null)
    try {
      const r = await post<Me | MfaChallenge>('/auth/login', { email, password })
      if ('mfa_required' in r) { setMfaToken(r.mfa_token); setMethods(r.methods ?? ['totp', 'recovery']); setPassword('') } else done(r)
    } catch (x) { setErr(x) } finally { setBusy(false) }
  }
  const verify = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(null)
    try { done(await post<Me>('/auth/login/verify', { mfa_token: mfaToken, code: code.trim() })) }
    catch (x) { setErr(x); setCode('') } finally { setBusy(false) }
  }
  const usePasskey = async () => {
    setBusy(true); setErr(null)
    try {
      const b = await post<{ ceremony_id: string; options: { publicKey: never } }>('/auth/passkey/begin')
      const credential = await getCredential(b.options)
      done(await post<Me>('/auth/passkey/finish', { ceremony_id: b.ceremony_id, credential }))
    } catch (x) { setErr(x instanceof ApiError ? x : new Error(waErrMsg(x))) } finally { setBusy(false) }
  }
  const useKey = async () => {
    setBusy(true); setErr(null)
    try {
      const b = await post<{ ceremony_id: string; options: { publicKey: never } }>('/auth/login/webauthn/begin', { mfa_token: mfaToken })
      const credential = await getCredential(b.options)
      done(await post<Me>('/auth/login/webauthn/finish', { mfa_token: mfaToken, ceremony_id: b.ceremony_id, credential }))
    } catch (x) { setErr(x instanceof ApiError ? x : new Error(waErrMsg(x))) } finally { setBusy(false) }
  }
  if (mfaToken) {
    const canKey = methods.includes('webauthn') && webauthnSupported()
    const canTotp = methods.includes('totp')
    return (
      <div className="login">
        <form className="card stack" onSubmit={verify}>
          <h1>Verifikasi 2 langkah</h1>
          {canKey && <div className="stack"><p className="muted">Gunakan kunci keamanan atau passkey Anda.</p><button type="button" className="primary" disabled={busy} onClick={useKey}>Gunakan kunci keamanan</button><p className="muted">atau</p></div>}
          <p className="muted">{canTotp ? 'Masukkan kode 6 digit dari aplikasi authenticator, atau satu kode pemulihan.' : 'Masukkan satu kode pemulihan.'}</p>
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
        {passkeyOk && <><p className="muted" style={{ textAlign: 'center', margin: 0 }}>atau</p>
          <button type="button" disabled={busy} onClick={usePasskey}>Masuk dengan passkey</button></>}
      </form>
    </div>
  )
}
