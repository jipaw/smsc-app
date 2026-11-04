export const fmtTime = (iso?: string | null) =>
  iso ? new Date(iso).toLocaleString('id-ID', { dateStyle: 'short', timeStyle: 'medium' }) : '-'

export function rel(iso?: string | null): string {
  if (!iso) return '-'
  const s = Math.round((Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 5) return 'baru saja'
  if (s < 60) return `${s} dtk lalu`
  if (s < 3600) return `${Math.floor(s / 60)} mnt lalu`
  if (s < 86400) return `${Math.floor(s / 3600)} jam lalu`
  return fmtTime(iso)
}

export const pct = (v: number) => `${(v * 100).toFixed(1)}%`

export const STATUS_LABEL: Record<string, { label: string; icon: string; tone: string }> = {
  queued: { label: 'Antre', icon: '◔', tone: 'muted' },
  assigned: { label: 'Dialokasikan', icon: '→', tone: 'info' },
  sending: { label: 'Mengirim', icon: '↗', tone: 'info' },
  sent: { label: 'Terkirim', icon: '✓', tone: 'ok-soft' },
  delivered: { label: 'Sampai', icon: '✓✓', tone: 'ok' },
  failed: { label: 'Gagal', icon: '✗', tone: 'bad' },
  cancelled: { label: 'Dibatalkan', icon: '⊘', tone: 'muted' },
  expired: { label: 'Kedaluwarsa', icon: '⌛', tone: 'warn' },
  unknown: { label: 'Tidak pasti', icon: '?', tone: 'unknown' },
  partial: { label: 'Parsial', icon: '◐', tone: 'warn' },
  online: { label: 'Online', icon: '●', tone: 'ok' },
  degraded: { label: 'Terganggu', icon: '▲', tone: 'warn' },
  offline: { label: 'Offline', icon: '○', tone: 'muted' },
  pending: { label: 'Menunggu', icon: '◔', tone: 'muted' },
  revoked: { label: 'Dicabut', icon: '⊘', tone: 'bad' },
  active: { label: 'Aktif', icon: '●', tone: 'ok' },
  no_service: { label: 'Tanpa sinyal', icon: '▲', tone: 'warn' },
  absent: { label: 'Tidak ada', icon: '○', tone: 'muted' },
  disabled: { label: 'Nonaktif', icon: '⊘', tone: 'muted' },
}

export const ERROR_LABEL: Record<string, string> = {
  no_service: 'Tidak ada sinyal', radio_off: 'Mode pesawat/radio mati', generic_failure: 'Kegagalan operator/pulsa',
  os_rate_limited: 'Dibatasi Android', invalid_destination: 'Nomor ditolak', short_code_blocked: 'Short code diblokir',
  permission_denied: 'Izin SMS dicabut', sim_unavailable: 'SIM tidak tersedia', delivery_failed: 'Laporan operator: gagal',
  ack_timeout: 'HP tidak menjawab', sent_timeout: 'Tidak ada kabar dari HP', device_lost: 'HP terputus saat mengirim',
  device_revoked: 'HP dicabut', expired: 'Melewati masa berlaku', suppressed: 'Di daftar blokir', null_pdu: 'Pesan tidak valid',
}
