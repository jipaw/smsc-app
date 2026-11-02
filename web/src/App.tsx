import { useEffect, useState } from 'react'
import { Route, Routes, Navigate } from 'react-router-dom'
import { ApiError, get, setCsrf } from './lib/api'
import { MeContext } from './lib/session'
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

export function App() {
  const [me, setMe] = useState<Me | null | undefined>(undefined) // undefined = memeriksa sesi
  useEffect(() => {
    get<Me>('/auth/me').then((m) => { setCsrf(m.csrf_token); setMe(m) }).catch((e) => { if (e instanceof ApiError && e.status === 401) setMe(null); else setMe(null) })
  }, [])
  if (me === undefined) return <div style={{ padding: 24 }}><Loading what="sesi" /></div>
  if (me === null) return <Login onLogin={setMe} />
  return (
    <MeContext.Provider value={me}>
      <Routes>
        <Route element={<Layout onLogout={() => { setCsrf(''); setMe(null) }} />}>
          <Route index element={<Overview />} />
          <Route path="devices" element={<Devices />} /><Route path="devices/:id" element={<DeviceDetail />} />
          <Route path="sims" element={<Sims />} /><Route path="send" element={<Send />} />
          <Route path="transactions" element={<Transactions />} /><Route path="campaigns" element={<Campaigns />} /><Route path="campaigns/new" element={<CampaignWizard />} /><Route path="campaigns/:id/edit" element={<CampaignWizard />} /><Route path="campaigns/:id" element={<CampaignDetail />} />
          <Route path="templates" element={<Templates />} /><Route path="settings" element={<Settings />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
    </MeContext.Provider>
  )
}
