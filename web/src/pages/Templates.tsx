import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { del, get, post, put } from '../lib/api'
import { analyze, variables } from '../lib/sms'
import { canOperate, useMe } from '../lib/session'
import type { Template } from '../lib/types'
import { Empty, ErrorBox, errMsg, Field, Loading, Modal, useToast } from '../components/ui'

export function Templates() {
  const me = useMe()
  const qc = useQueryClient()
  const toast = useToast()
  const [edit, setEdit] = useState<Partial<Template> | null>(null)
  const q = useQuery({ queryKey: ['templates'], queryFn: () => get<{ items: Template[] }>('/templates') })
  const save = useMutation({
    mutationFn: (t: Partial<Template>) => (t.id ? put(`/templates/${t.id}`, { name: t.name, body: t.body }) : post('/templates', { name: t.name, body: t.body })),
    onSuccess: () => { toast('Template disimpan'); setEdit(null); qc.invalidateQueries({ queryKey: ['templates'] }) },
  })
  const rm = useMutation({ mutationFn: (id: string) => del(`/templates/${id}`), onSuccess: () => qc.invalidateQueries({ queryKey: ['templates'] }), onError: (e) => toast(errMsg(e), true) })
  const op = canOperate(me)
  const info = analyze(edit?.body ?? '')
  return (
    <div className="stack gap-lg">
      <div className="row spread"><h1>Template</h1>{op && <button className="primary" onClick={() => setEdit({ name: '', body: '' })}>+ Template</button>}</div>
      {q.isLoading && <Loading />}<ErrorBox error={q.error} />
      {q.data?.items.length === 0 && <Empty>Belum ada template. Gunakan {'{{nama}}'} untuk variabel.</Empty>}
      <div className="grid cols-2">{q.data?.items.map((t) => (
        <div key={t.id} className="card stack"><div className="row spread"><h3>{t.name}</h3>{op && <span className="row"><button className="small" onClick={() => setEdit(t)}>Ubah</button><button className="small danger" onClick={() => window.confirm(`Hapus template "${t.name}"?`) && rm.mutate(t.id)}>Hapus</button></span>}</div>
          <div style={{ whiteSpace: 'pre-wrap' }}>{t.body}</div><div className="hint">Variabel: {t.variables.length ? t.variables.join(', ') : '—'}</div></div>))}</div>
      <Modal open={!!edit} onClose={() => setEdit(null)} title={edit?.id ? 'Ubah template' : 'Template baru'}>
        <Field label="Nama"><input value={edit?.name ?? ''} onChange={(e) => setEdit({ ...edit, name: e.target.value })} /></Field>
        <Field label="Isi" hint={`${info.encoding === 'gsm7' ? 'GSM-7' : 'UCS-2'} · ${info.units} unit · ${info.parts} segmen · variabel: ${variables(edit?.body ?? '').join(', ') || '—'}`}>
          <textarea value={edit?.body ?? ''} onChange={(e) => setEdit({ ...edit, body: e.target.value })} placeholder="Halo {{name}}, tagihan {{amount}} jatuh tempo {{due}}." />
        </Field>
        <ErrorBox error={save.error} />
        <div className="row" style={{ justifyContent: 'flex-end' }}><button onClick={() => setEdit(null)}>Batal</button><button className="primary" disabled={save.isPending} onClick={() => edit && save.mutate(edit)}>Simpan</button></div>
      </Modal>
    </div>
  )
}
