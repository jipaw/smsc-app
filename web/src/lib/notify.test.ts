import { describe, expect, it } from 'vitest'
import { bellLabel, notificationTarget, notificationText } from './notify'

describe('notificationText', () => {
  it('tampilan dibagikan', () => {
    expect(notificationText({ kind: 'saved_view.shared', data: { owner: 'budi', name: 'Gagal tim' } })).toBe('budi membagikan tampilan transaksi “Gagal tim”')
  })
  it('tampilan dimoderasi menyebut admin', () => {
    expect(notificationText({ kind: 'saved_view.moderated', data: { name: 'Luas', by: 'admin' } })).toBe('Tampilan berbagi Anda “Luas” dihapus oleh admin admin')
    expect(notificationText({ kind: 'saved_view.moderated', data: { name: 'Luas' } })).toContain('(tidak diketahui)')
  })
  it('data tak terduga tidak merusak: bukan string diabaikan, string panjang dipotong, jenis asing netral', () => {
    expect(notificationText({ kind: 'saved_view.shared', data: { owner: 123, name: 'x'.repeat(500) } })).toBe(`Seseorang membagikan tampilan transaksi “${'x'.repeat(60)}”`)
    expect(notificationText({ kind: 'masa.depan' })).toBe('Notifikasi baru')
  })
  it('target dan label lonceng', () => {
    expect(notificationTarget({ kind: 'saved_view.shared' })).toBe('/transactions')
    expect(notificationTarget({ kind: 'saved_view.moderated' })).toBeNull()
    expect(bellLabel(0)).toContain('tidak ada yang baru')
    expect(bellLabel(3)).toBe('Notifikasi, 3 belum dibaca')
  })
})
