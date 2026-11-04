import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

export type SseState = 'connecting' | 'live' | 'down'

/** Jeda sambung ulang (ms) setelah `attempt` kegagalan beruntun: 1 dtk, 2, 4, … maksimum 30 dtk. */
export function reconnectDelay(attempt: number): number {
  return Math.min(30_000, 1000 * 2 ** Math.max(0, Math.min(attempt, 10)))
}

/**
 * Satu EventSource global. Perubahan di-coalesce 400 ms lalu meng-invalidate query terkait (ADR-08).
 * EventSource hanya menyambung ulang otomatis untuk putus jaringan; respons HTTP bukan-200 (mis. 429, 401 sesaat) MENUTUP-nya
 * selamanya, sehingga di sini disambung ulang secara manual dengan backoff dan seluruh query disegarkan (event mungkin terlewat).
 */
export function useEventStream(): SseState {
  const qc = useQueryClient()
  const [state, setState] = useState<SseState>('connecting')
  useEffect(() => {
    let es: EventSource | null = null
    let stopped = false
    let attempt = 0
    let hadFailure = false
    let retry: number | undefined
    const dirty = new Set<string>()
    let timer: number | undefined
    const flush = () => {
      timer = undefined
      for (const k of dirty) qc.invalidateQueries({ queryKey: [k] })
      dirty.clear()
    }
    const mark = (...keys: string[]) => {
      keys.forEach((k) => dirty.add(k))
      if (timer === undefined) timer = window.setTimeout(flush, 400)
    }
    const connect = () => {
      if (stopped) return
      const src = new EventSource('/api/v1/events/stream')
      es = src
      src.onopen = () => {
        setState('live')
        attempt = 0
        if (hadFailure) { hadFailure = false; qc.invalidateQueries() } // event selama putus mungkin terlewat
      }
      src.onerror = () => {
        if (src.readyState === EventSource.CLOSED) { // ditolak server: tidak akan menyambung sendiri
          setState('down')
          hadFailure = true
          src.close()
          retry = window.setTimeout(connect, reconnectDelay(attempt++))
        } else setState('connecting')
      }
      src.addEventListener('message.updated', () => mark('messages', 'message', 'overview', 'series', 'campaigns', 'campaign'))
      src.addEventListener('campaign.updated', () => mark('campaigns', 'campaign', 'overview'))
      src.addEventListener('device.updated', () => mark('devices', 'device', 'overview', 'sims'))
      src.addEventListener('sim.updated', () => mark('devices', 'sims', 'device'))
      src.addEventListener('notification.created', () => mark('notifications'))
      src.addEventListener('settings.updated', () => mark('overview', 'pause'))
      src.addEventListener('resync', () => qc.invalidateQueries())
    }
    connect()
    return () => { stopped = true; es?.close(); if (timer) window.clearTimeout(timer); if (retry) window.clearTimeout(retry) }
  }, [qc])
  return state
}
