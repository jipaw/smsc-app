import { useEffect, useState } from 'react'
import { Route, Routes, Navigate } from 'react-router-dom'
import { ApiError, get, post, setCsrf } from './lib/api'
import { MeContext, MeRefreshContext } from './lib/session'
import type { Me } from './lib/types'
import { Layout } from './components/Layout'
import { Loading } from './components/ui'
import { Login } from './pages/Login'
import { Overview } from './pages/Overview'
import { DeviceDetail, Devices, Sims } from './pages/Devices'
import { Send } from './pages/Send'
import { Transactions } from './pages/Transactions'
import { Templates } from './pages/Templates'
import { CampaignDetail, CampaignWizard, Campaigns } from './pages/Campaigns'
import { Settings } from './pages/Settings'
import { Account } from './pages/Account'

export function App() {
  const [me, setMe] = useState<Me | null | undefined>(undefined) // undefined = memeriksa sesi
  useEffect(() => {
    get<Me>('/auth/me').then((m) => { setCsrf(m.csrf_token); setMe(m) }).catch((e) => { if (e instanceof ApiError && e.status === 401) setMe(null); else setMe(null) })
  }, [])
  if (me === undefined) return <div style={{ padding: 24 }}><Loading what="sesi" /></div>
  if (me === null) return <Login onLogin={setMe} />
  const refreshMe = async () => { const m = await get<Me>('/auth/me'); setCsrf(m.csrf_token); setMe(m) }
  const logout = async () => { try { await post('/auth/logout') } finally { setCsrf(''); setMe(null) } }
  // kebijakan mewajibkan 2FA tetapi akun ini belum punya: hanya halaman Akun (server juga memblok endpoint lain)
  if (me.mfa_setup_required) {
    return (
      <MeContext.Provider value={me}><MeRefreshContext.Provider value={refreshMe}>
        <div className="shell"><div className="main">
          <header className="top"><strong>SMSC Gateway</strong><div className="row"><span className="muted">{me.email} · {me.role}</span><button className="small" onClick={logout}>Keluar</button></div></header>
          <main className="content"><Account /></main>
        </div></div>
      </MeRefreshContext.Provider></MeContext.Provider>
    )
  }
  return (
    <MeContext.Provider value={me}><MeRefreshContext.Provider value={refreshMe}>
      <Routes>
        <Route element={<Layout onLogout={() => { setCsrf(''); setMe(null) }} />}>
          <Route index element={<Overview />} />
          <Route path="devices" element={<Devices />} /><Route path="devices/:id" element={<DeviceDetail />} />
          <Route path="sims" element={<Sims />} /><Route path="send" element={<Send />} />
          <Route path="transactions" element={<Transactions />} /><Route path="campaigns" element={<Campaigns />} /><Route path="campaigns/new" element={<CampaignWizard />} /><Route path="campaigns/:id/edit" element={<CampaignWizard />} /><Route path="campaigns/:id" element={<CampaignDetail />} />
          <Route path="templates" element={<Templates />} /><Route path="settings" element={<Settings />} /><Route path="account" element={<Account />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
    </MeRefreshContext.Provider></MeContext.Provider>
  )
}
