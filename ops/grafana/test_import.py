#!/usr/bin/env python3
"""Uji skrip impor terhadap Grafana PALSU (http.server lokal). Memverifikasi perilaku skrip — BUKAN Grafana sungguhan.
Jalankan: python3 ops/grafana/test_import.py"""
import io, json, os, sys, threading, contextlib, pathlib
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import import_dashboards as imp

STATE = {"calls": [], "folders": [], "dash": {}, "ds": [{"uid": "ds1", "name": "Prom", "type": "prometheus"}], "token": "tok", "drop_panels": False}


class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass

    def _send(self, code, obj):
        raw = json.dumps(obj).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(raw))); self.end_headers(); self.wfile.write(raw)

    def _body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(n)) if n else None

    def do_GET(self):
        STATE["calls"].append(("GET", self.path, self.headers.get("Authorization")))
        if self.path == "/api/health": return self._send(200, {"version": "11.0.0"})
        if self.headers.get("Authorization") != "Bearer " + STATE["token"]: return self._send(401, {"message": "Unauthorized"})
        if self.path == "/api/datasources": return self._send(200, STATE["ds"])
        if self.path == "/api/folders": return self._send(200, STATE["folders"])
        if self.path.startswith("/api/dashboards/uid/"):
            d = STATE["dash"].get(self.path.rsplit("/", 1)[1])
            return self._send(200, {"dashboard": d}) if d else self._send(404, {"message": "not found"})
        self._send(404, {})

    def do_POST(self):
        b = self._body()
        STATE["calls"].append(("POST", self.path, self.headers.get("Authorization")))
        if self.headers.get("Authorization") != "Bearer " + STATE["token"]: return self._send(401, {"message": "Unauthorized"})
        if self.path == "/api/folders":
            f = {"uid": "f1", "title": b["title"]}; STATE["folders"].append(f); return self._send(200, f)
        if self.path == "/api/dashboards/import":
            d = dict(b["dashboard"])
            STATE["last_import"] = b
            if STATE["drop_panels"]: d["panels"] = d["panels"][:1]
            STATE["dash"][d["uid"]] = d
            return self._send(200, {"importedUrl": f"/d/{d['uid']}/x"})
        self._send(404, {})


srv = HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
URL = f"http://127.0.0.1:{srv.server_port}"
fails = []
def check(c, n):
    print(("ok   " if c else "FAIL ") + n)
    if not c: fails.append(n)

def run(args, token="tok"):
    os.environ["GRAFANA_TOKEN"] = token
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        try: rc = imp.main(["--url", URL] + args)
        except SystemExit as e: rc = e.code if isinstance(e.code, int) else (print(e.code) or 2)
    return rc, out.getvalue()

STATE["calls"].clear()
rc, out = run(["--dry-run"])
check(rc == 0 and not any(c[0] == "POST" for c in STATE["calls"]), "dry-run tidak menulis apa pun")
check("smsc-delivery" in out and "smsc-scim" in out, "dry-run menyebut semua dashboard")

STATE["calls"].clear(); STATE["folders"].clear()
rc, out = run([])
check(rc == 0, "impor sukses (exit 0)")
imps = [c for c in STATE["calls"] if c[1] == "/api/dashboards/import"]
check(len(imps) == 2 and all(c[2] == "Bearer tok" for c in STATE["calls"] if c[1] != "/api/health"), "dua dashboard diimpor dengan header Authorization")
li = STATE["last_import"]
check(li["overwrite"] is True and li["inputs"] == [{"name": "DS_PROMETHEUS", "type": "datasource", "pluginId": "prometheus", "value": "ds1"}], "input DS_PROMETHEUS dipetakan ke uid data source, overwrite=true")
check(li["folderUid"] == "f1" and STATE["folders"][0]["title"] == "SMSC", "folder SMSC dibuat dan dipakai")
calls_before = len(STATE["calls"])
rc, out = run([])
check(rc == 0 and sum(1 for c in STATE["calls"][calls_before:] if c[:2] == ("POST", "/api/folders")) == 0, "folder yang sudah ada dipakai ulang (idempoten)")

rc, out = run([], token="salah")
check(rc != 0, "token salah => gagal (exit non-nol)")
check("tok" not in out and "salah" not in out, "token tidak pernah dicetak")

STATE["drop_panels"] = True
rc, out = run([])
check(rc == 1 and "GAGAL" in out, "verifikasi baca-balik mendeteksi panel hilang")
STATE["drop_panels"] = False

STATE["ds"].append({"uid": "ds2", "name": "Prom2", "type": "prometheus"})
rc, out = run([])
check(rc != 0 and "--datasource" in out, "dua data source Prometheus => minta dipilih")
rc, out = run(["--datasource", "Prom2"])
check(rc == 0 and STATE["last_import"]["inputs"][0]["value"] == "ds2", "--datasource memilih berdasarkan nama")
print("SEMUA LULUS" if not fails else f"{len(fails)} GAGAL")
sys.exit(1 if fails else 0)
