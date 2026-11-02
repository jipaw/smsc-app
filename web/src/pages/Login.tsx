import { FormEvent, useState } from 'react'
import { post, setCsrf } from '../lib/api'
import type { Me } from '../lib/types'
import { ErrorBox } from '../components/ui'

export function Login({ onLogin }: { onLogin: (m: Me) => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [err, setErr] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr(null)
    try {
      const me = await post<Me>('/auth/login', { email, password })
      setCsrf(me.csrf_token)
      onLogin(me)
    } catch (x) { setErr(x) } finally { setBusy(false) }
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
