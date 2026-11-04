import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'

export type SseState = 'connecting' | 'live' | 'down'

/** Satu EventSource global. Perubahan di-coalesce 400 ms lalu meng-invalidate query terkait (ADR-08). */
export function useEventStream(): SseState {
  const qc = useQueryClient()
  const [state, setState] = useState<SseState>('connecting')
  useEffect(() => {
    const es = new EventSource('/api/v1/events/stream')
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
    es.onopen = () => setState('live')
    es.onerror = () => setState(es.readyState === EventSource.CLOSED ? 'down' : 'connecting')
    es.addEventListener('message.updated', () => mark('messages', 'message', 'overview', 'series', 'campaigns', 'campaign'))
    es.addEventListener('campaign.updated', () => mark('campaigns', 'campaign', 'overview'))
    es.addEventListener('device.updated', () => mark('devices', 'device', 'overview', 'sims'))
    es.addEventListener('sim.updated', () => mark('devices', 'sims', 'device'))
    es.addEventListener('notification.created', () => mark('notifications'))
    es.addEventListener('settings.updated', () => mark('overview', 'pause'))
    es.addEventListener('resync', () => qc.invalidateQueries())
    return () => { es.close(); if (timer) window.clearTimeout(timer) }
  }, [qc])
  return state
}
