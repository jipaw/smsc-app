#!/usr/bin/env python3
"""Impor dashboard ops/grafana/*.json ke instance Grafana lewat HTTP API.

Pemakaian (jalankan dari mesin yang dapat menjangkau Grafana):
  GRAFANA_URL=http://10.18.0.14:3000 GRAFANA_TOKEN=glsa_xxx python3 ops/grafana/import_dashboards.py [--dry-run]
                   [--datasource NAMA_ATAU_UID] [--folder "SMSC"] [files...]

Token = service-account token (peran Editor cukup) dibaca dari environment GRAFANA_TOKEN — tidak pernah dari argumen CLI
agar tidak muncul di daftar proses/riwayat shell. Skrip: cek /api/health, pilih data source Prometheus (otomatis bila hanya
satu), buat/temukan folder, impor tiap dashboard (menimpa uid yang sama) memetakan input DS_PROMETHEUS, lalu memverifikasi
dengan membaca balik dashboard dan membandingkan jumlah panel. Exit 1 bila ada yang gagal."""
import argparse, json, os, pathlib, sys, urllib.error, urllib.request

ROOT = pathlib.Path(__file__).resolve().parent


class Api:
    def __init__(self, base, token, timeout=15):
        self.base, self.token, self.timeout = base.rstrip("/"), token, timeout

    def call(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Accept", "application/json")
        if data is not None:
            req.add_header("Content-Type", "application/json")
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as r:
                raw = r.read()
                return r.status, (json.loads(raw) if raw else None)
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                return e.code, json.loads(raw)
            except Exception:
                return e.code, {"message": raw[:200].decode("utf-8", "replace")}


def pick_datasource(api, want):
    st, ds = api.call("GET", "/api/datasources")
    if st != 200:
        raise SystemExit(f"GAGAL daftar data source: HTTP {st} {ds}")
    prom = [d for d in ds if d.get("type") == "prometheus"]
    if want:
        m = [d for d in prom if want in (d.get("uid"), d.get("name"))]
        if not m:
            raise SystemExit(f"data source Prometheus '{want}' tidak ada; tersedia: {[d['name'] for d in prom]}")
        return m[0]
    if len(prom) != 1:
        raise SystemExit(f"ada {len(prom)} data source Prometheus ({[d['name'] for d in prom]}); pilih dengan --datasource")
    return prom[0]


def ensure_folder(api, title):
    st, fs = api.call("GET", "/api/folders")
    if st != 200:
        raise SystemExit(f"GAGAL daftar folder: HTTP {st} {fs}")
    for f in fs:
        if f.get("title") == title:
            return f["uid"]
    st, f = api.call("POST", "/api/folders", {"title": title})
    if st not in (200, 201):
        raise SystemExit(f"GAGAL membuat folder: HTTP {st} {f}")
    return f["uid"]


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("files", nargs="*")
    ap.add_argument("--url", default=os.environ.get("GRAFANA_URL", ""))
    ap.add_argument("--datasource", default="")
    ap.add_argument("--folder", default="SMSC")
    ap.add_argument("--dry-run", action="store_true", help="hanya baca (health, data source, folder yang ada); tidak menulis apa pun")
    a = ap.parse_args(argv)
    if not a.url:
        raise SystemExit("set GRAFANA_URL atau --url")
    token = os.environ.get("GRAFANA_TOKEN", "")
    files = [pathlib.Path(f) for f in a.files] or sorted(ROOT.glob("*.json"))
    dashboards = [(f, json.loads(f.read_text())) for f in files]
    api = Api(a.url, token)

    st, h = api.call("GET", "/api/health")
    if st != 200:
        raise SystemExit(f"Grafana tidak sehat/terjangkau: HTTP {st} {h}")
    print(f"Grafana {h.get('version', '?')} di {a.url}")
    if not token:
        print("PERINGATAN: GRAFANA_TOKEN kosong; hanya berhasil bila instance mengizinkan akses anonim Editor")
    ds = pick_datasource(api, a.datasource)
    print(f"data source Prometheus: {ds['name']} (uid {ds['uid']})")
    if a.dry_run:
        print(f"dry-run: {len(dashboards)} dashboard akan diimpor ke folder '{a.folder}': " + ", ".join(d['uid'] for _, d in dashboards))
        return 0

    folder_uid = ensure_folder(api, a.folder)
    bad = 0
    for f, d in dashboards:
        body = {"dashboard": {**d, "id": None}, "overwrite": True, "folderUid": folder_uid,
                "inputs": [{"name": i["name"], "type": "datasource", "pluginId": "prometheus", "value": ds["uid"]} for i in d.get("__inputs", [])]}
        st, r = api.call("POST", "/api/dashboards/import", body)
        if st != 200:
            print(f"GAGAL {f.name}: HTTP {st} {r}")
            bad += 1
            continue
        st, got = api.call("GET", f"/api/dashboards/uid/{d['uid']}")
        n_src = len([p for p in d["panels"]])
        n_got = len(got["dashboard"].get("panels", [])) if st == 200 else -1
        ok = st == 200 and n_got == n_src
        print(f"{'ok  ' if ok else 'GAGAL'} {f.name}: uid {d['uid']}, panel {n_got}/{n_src}, {a.url.rstrip('/')}{r.get('importedUrl', '')}")
        bad += 0 if ok else 1
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
