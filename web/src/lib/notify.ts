/** Teks notifikasi dalam aplikasi. Nilai data berasal dari pengguna lain: hanya string dan dirender sebagai teks biasa (React meng-escape). */
export interface NotificationLike { kind: string; data?: Record<string, unknown> | null }

const str = (v: unknown, max = 60): string => (typeof v === 'string' ? v.slice(0, max) : '')

export function notificationText(n: NotificationLike): string {
  const d = n.data ?? {}
  switch (n.kind) {
    case 'saved_view.shared': return `${str(d.owner) || 'Seseorang'} membagikan tampilan transaksi “${str(d.name)}”`
    case 'saved_view.moderated': return `Tampilan berbagi Anda “${str(d.name)}” dihapus oleh admin ${str(d.by) || '(tidak diketahui)'}`
    default: return 'Notifikasi baru'
  }
}

/** Tujuan saat notifikasi dibuka (null = tidak ada). */
export function notificationTarget(n: NotificationLike): string | null {
  return n.kind === 'saved_view.shared' ? '/transactions' : null
}

/** Teks tombol lonceng untuk pembaca layar. */
export function bellLabel(unread: number): string {
  return unread > 0 ? `Notifikasi, ${unread} belum dibaca` : 'Notifikasi, tidak ada yang baru'
}
