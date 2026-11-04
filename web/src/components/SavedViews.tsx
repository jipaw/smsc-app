import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { del, get, post, put } from '../lib/api'
import type { SavedView } from '../lib/types'
import { fromView, sameFilter, toView, type TxFilter } from '../lib/txfilter'
import { errMsg, useToast } from './ui'

/** Tampilan (filter) transaksi tersimpan per pengguna di server (maks. 20). Menerapkan tampilan = mengganti filter di URL. */
export function SavedViews({ filter, apply }: { filter: TxFilter; apply: (f: TxFilter) => void }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [name, setName] = useState('')
  const q = useQuery({ queryKey: ['saved-views'], queryFn: () => get<{ items: SavedView[] }>('/saved-views') })
  const views = q.data?.items ?? []
  const current = views.find((v) => sameFilter(fromView(v.filter), filter))
  const done = (msg: string) => { toast(msg); qc.invalidateQueries({ queryKey: ['saved-views'] }) }
  const create = useMutation({
    mutationFn: () => post<SavedView>('/saved-views', { name: name.trim(), filter: toView(filter) }),
    onSuccess: () => { setName(''); done('Tampilan disimpan') }, onError: (e) => toast(errMsg(e), true),
  })
  const update = useMutation({
    mutationFn: (v: SavedView) => put<SavedView>(`/saved-views/${v.id}`, { name: v.name, filter: toView(filter) }),
    onSuccess: () => done('Tampilan diperbarui'), onError: (e) => toast(errMsg(e), true),
  })
  const remove = useMutation({
    mutationFn: (v: SavedView) => del(`/saved-views/${v.id}`),
    onSuccess: () => done('Tampilan dihapus'), onError: (e) => toast(errMsg(e), true),
  })
  const selected = views.find((v) => v.id === (current?.id ?? ''))
  return (
    <div className="row" role="group" aria-label="Kelola tampilan tersimpan">
      <select aria-label="Tampilan tersimpan" value={current?.id ?? ''} onChange={(e) => { const v = views.find((x) => x.id === e.target.value); if (v) apply(fromView(v.filter)) }}>
        <option value="">{views.length ? 'Tampilan tersimpan…' : 'Belum ada tampilan tersimpan'}</option>
        {views.map((v) => <option key={v.id} value={v.id}>{v.name}</option>)}
      </select>
      {selected && <>
        <button className="small" onClick={() => remove.mutate(selected)} disabled={remove.isPending}>Hapus tampilan</button>
      </>}
      <input aria-label="Nama tampilan" placeholder="Nama tampilan" maxLength={60} value={name} onChange={(e) => setName(e.target.value)} />
      <button className="small" disabled={!name.trim() || create.isPending} onClick={() => create.mutate()}>Simpan tampilan</button>
      {!selected && views.length > 0 && <UpdateHint views={views} filter={filter} update={update.mutate} pending={update.isPending} />}
    </div>
  )
}

/** Menimpa tampilan yang ada dengan filter saat ini (pilih tampilan, lalu "Perbarui"). */
function UpdateHint({ views, filter, update, pending }: { views: SavedView[]; filter: TxFilter; update: (v: SavedView) => void; pending: boolean }) {
  const [id, setId] = useState('')
  const v = views.find((x) => x.id === id)
  return (
    <>
      <select aria-label="Timpa tampilan" value={id} onChange={(e) => setId(e.target.value)}><option value="">Timpa tampilan…</option>{views.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select>
      <button className="small" disabled={!v || pending || sameFilter(fromView(v.filter), filter)} onClick={() => v && update(v)}>Perbarui</button>
    </>
  )
}
