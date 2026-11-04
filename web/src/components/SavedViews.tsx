import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { del, get, post, put } from '../lib/api'
import type { SavedView } from '../lib/types'
import { fromView, sameFilter, shareBlocker, toView, type TxFilter } from '../lib/txfilter'
import { isAdmin, useMe } from '../lib/session'
import { errMsg, useToast } from './ui'

/**
 * Tampilan (filter) transaksi tersimpan di server: milik sendiri (maks. 20) dan yang dibagikan pengguna lain (hanya-baca).
 * Menerapkan tampilan = mengganti filter di URL. Membagikan ke tim tidak diizinkan untuk filter nomor (viewer melihat nomor termasking).
 */
export function SavedViews({ filter, apply }: { filter: TxFilter; apply: (f: TxFilter) => void }) {
  const me = useMe()
  const qc = useQueryClient()
  const toast = useToast()
  const [name, setName] = useState('')
  const [share, setShare] = useState(false)
  const q = useQuery({ queryKey: ['saved-views'], queryFn: () => get<{ items: SavedView[] }>('/saved-views') })
  const views = q.data?.items ?? []
  const mine = views.filter((v) => v.mine)
  const others = views.filter((v) => !v.mine)
  const blocker = shareBlocker(filter)
  const current = views.find((v) => sameFilter(fromView(v.filter), filter))
  const done = (msg: string) => { toast(msg); qc.invalidateQueries({ queryKey: ['saved-views'] }) }
  const create = useMutation({
    mutationFn: () => post<SavedView>('/saved-views', { name: name.trim(), filter: toView(filter), shared: share && !blocker }),
    onSuccess: () => { setName(''); setShare(false); done('Tampilan disimpan') }, onError: (e) => toast(errMsg(e), true),
  })
  const update = useMutation({
    mutationFn: (v: SavedView) => put<SavedView>(`/saved-views/${v.id}`, { name: v.name, filter: toView(filter) }),
    onSuccess: () => done('Tampilan diperbarui'), onError: (e) => toast(errMsg(e), true),
  })
  const toggleShare = useMutation({
    mutationFn: (v: SavedView) => put<SavedView>(`/saved-views/${v.id}`, { name: v.name, filter: toView(fromView(v.filter)), shared: !v.shared }),
    onSuccess: (v) => done(v.shared ? 'Tampilan dibagikan ke tim' : 'Tampilan tidak lagi dibagikan'), onError: (e) => toast(errMsg(e), true),
  })
  const remove = useMutation({
    mutationFn: (v: SavedView) => del(`/saved-views/${v.id}`),
    onSuccess: () => done('Tampilan dihapus'), onError: (e) => toast(errMsg(e), true),
  })
  const canDelete = current && (current.mine || (current.shared && isAdmin(me)))
  return (
    <div className="row" role="group" aria-label="Kelola tampilan tersimpan">
      <select aria-label="Tampilan tersimpan" value={current?.id ?? ''} onChange={(e) => { const v = views.find((x) => x.id === e.target.value); if (v) apply(fromView(v.filter)) }}>
        <option value="">{views.length ? 'Tampilan tersimpan…' : 'Belum ada tampilan tersimpan'}</option>
        {mine.length > 0 && <optgroup label="Milik saya">{mine.map((v) => <option key={v.id} value={v.id}>{v.name}{v.shared ? ' (dibagikan)' : ''}</option>)}</optgroup>}
        {others.length > 0 && <optgroup label="Dibagikan tim">{others.map((v) => <option key={v.id} value={v.id}>{v.name} — oleh {v.owner}</option>)}</optgroup>}
      </select>
      {current?.mine && (
        <label className="row" style={{ gap: 4 }}>
          <input type="checkbox" checked={current.shared} disabled={toggleShare.isPending || (!current.shared && !!shareBlocker(fromView(current.filter)))} onChange={() => toggleShare.mutate(current)} />Bagikan ke tim
        </label>)}
      {current && !current.mine && <span className="hint">dibagikan oleh {current.owner} (hanya-baca)</span>}
      {canDelete && <button className="small" onClick={() => remove.mutate(current)} disabled={remove.isPending}>Hapus tampilan</button>}
      <input aria-label="Nama tampilan" placeholder="Nama tampilan" maxLength={60} value={name} onChange={(e) => setName(e.target.value)} />
      <label className="row" style={{ gap: 4 }} title={blocker || undefined}><input type="checkbox" checked={share && !blocker} disabled={!!blocker} onChange={(e) => setShare(e.target.checked)} />Bagikan saat disimpan</label>
      <button className="small" disabled={!name.trim() || create.isPending} onClick={() => create.mutate()}>Simpan tampilan</button>
      {!current && mine.length > 0 && <UpdatePicker views={mine} filter={filter} update={update.mutate} pending={update.isPending} />}
    </div>
  )
}

/** Menimpa salah satu tampilan milik sendiri dengan filter saat ini (status berbagi dipertahankan). */
function UpdatePicker({ views, filter, update, pending }: { views: SavedView[]; filter: TxFilter; update: (v: SavedView) => void; pending: boolean }) {
  const [id, setId] = useState('')
  const v = views.find((x) => x.id === id)
  return (
    <>
      <select aria-label="Timpa tampilan" value={id} onChange={(e) => setId(e.target.value)}><option value="">Timpa tampilan…</option>{views.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select>
      <button className="small" disabled={!v || pending || sameFilter(fromView(v.filter), filter)} onClick={() => v && update(v)}>Perbarui</button>
    </>
  )
}
