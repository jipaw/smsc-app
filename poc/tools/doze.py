#!/usr/bin/env python3
"""Uji R3 "Doze paksa" terukur: masukkan HP ke Doze lewat adb, pantau status device di smscd, kirim SMS saat HP tidur.

  py poc\\tools\\doze.py --minutes 20 --server https://smsc.tailnet.ts.net --email admin@contoh.id --send-to 0812xxxxxxx

Langkah: battery unplug → deviceidle force-idle → sampel tiap --interval detik (state Doze dari HP + status device dari
server) → pada --send-at menit kirim 1 SMS lewat API dan ukur waktu sampai sent/delivered → deviceidle unforce →
battery reset. Langkah pemulihan SELALU dijalankan (juga saat Ctrl+C atau error).
Hasil: doze.csv + doze_summary.txt di folder --out. Password dari env SMSC_PASSWORD atau ditanya.
Hanya stdlib Python 3.8+.
"""
import argparse
import csv
import datetime as dt
import getpass
import http.cookiejar
import json
import os
import pathlib
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request


class Adb:
    def __init__(self, serial=None):
        self.adb = shutil.which("adb") or sys.exit("adb tidak ditemukan di PATH")
        self.serial = serial

    def sh(self, cmd):
        args = [self.adb] + (["-s", self.serial] if self.serial else []) + ["shell", cmd]
        return subprocess.run(args, capture_output=True, text=True, timeout=60, encoding="utf-8", errors="replace").stdout.strip()


class Api:
    """Sesi dashboard (cookie + CSRF): endpoint devices butuh peran pengguna, bukan API key."""

    def __init__(self, base, email, password):
        self.base = base.rstrip("/")
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.csrf = ""
        me = self.call("POST", "/api/v1/auth/login", {"email": email, "password": password})
        if me.get("mfa_required"):
            sys.exit("akun memakai 2FA: pakai akun uji tanpa 2FA untuk skrip ini")
        self.csrf = me.get("csrf_token", "")

    def call(self, method, path, body=None):
        h = {"Content-Type": "application/json"}
        if self.csrf and method != "GET":
            h["X-CSRF-Token"] = self.csrf
        req = urllib.request.Request(self.base + path, data=json.dumps(body).encode() if body is not None else None, method=method, headers=h)
        try:
            with self.op.open(req, timeout=20) as r:
                return json.loads(r.read() or b"null")
        except urllib.error.HTTPError as e:
            raise RuntimeError(f"{method} {path}: HTTP {e.code} {e.read()[:200]!r}") from None


def device_status(api, device_id):
    if not api:
        return ""
    try:
        items = api.call("GET", "/api/v1/devices")["items"]
    except Exception as e:  # server tak terjangkau dicatat, bukan menghentikan uji
        return f"error:{e}"[:80]
    d = next((x for x in items if not device_id or x["id"] == device_id), None)
    return d["status"] if d else "tidak-ada"


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--minutes", type=float, default=20, help="lama Doze paksa (default 20)")
    ap.add_argument("--interval", type=float, default=30, help="detik antar sampel (default 30)")
    ap.add_argument("--serial")
    ap.add_argument("--server", help="URL smscd (dashboard), mis. https://smsc.tailnet.ts.net")
    ap.add_argument("--email", help="akun dashboard (operator/admin, tanpa 2FA)")
    ap.add_argument("--device-id", help="device yang dipantau (default: satu-satunya device)")
    ap.add_argument("--send-to", help="nomor tujuan MILIK ANDA untuk uji kirim saat tidur")
    ap.add_argument("--send-at", type=float, default=None, help="menit ke berapa SMS dikirim (default: tengah durasi)")
    ap.add_argument("--out", default=".", help="folder hasil (default: folder saat ini)")
    a = ap.parse_args(argv)
    adb = Adb(a.serial)
    api = None
    if a.server:
        if not a.email:
            sys.exit("--email wajib bila --server diisi")
        api = Api(a.server, a.email, os.environ.get("SMSC_PASSWORD") or getpass.getpass(f"password {a.email}: "))
    out = pathlib.Path(a.out)
    out.mkdir(parents=True, exist_ok=True)
    send_at = a.send_at if a.send_at is not None else a.minutes / 2
    rows, sent_id, send_t0, send_result = [], None, None, None
    t0 = time.time()

    def sample(phase):
        r = {"t_s": round(time.time() - t0, 1), "phase": phase, "deep": adb.sh("dumpsys deviceidle get deep"),
             "light": adb.sh("dumpsys deviceidle get light"), "server": device_status(api, a.device_id)}
        rows.append(r)
        print(f"{dt.datetime.now():%H:%M:%S} {phase:<8} deep={r['deep']:<10} light={r['light']:<10} server={r['server']}", flush=True)

    try:
        sample("sebelum")
        adb.sh("dumpsys battery unplug")
        adb.sh("dumpsys deviceidle force-idle")
        end = t0 + a.minutes * 60
        while time.time() < end:
            time.sleep(min(a.interval, max(end - time.time(), 0)))
            sample("doze")
            if api and a.send_to and sent_id is None and time.time() - t0 >= send_at * 60:
                r = api.call("POST", "/api/v1/messages", {"recipients": [{"to": a.send_to}], "body": "Uji Doze SMSC (abaikan)"})
                sent_id, send_t0 = r["messages"][0]["id"], time.time()
                print(f"  SMS uji dikirim saat Doze (message {sent_id})", flush=True)
            if sent_id and send_result is None:
                st = api.call("GET", f"/api/v1/messages/{sent_id}")["status"]
                if st in ("sent", "delivered", "failed"):
                    send_result = (st, round(time.time() - send_t0, 1))
                    print(f"  SMS uji: {st} setelah {send_result[1]} dtk", flush=True)
    finally:
        adb.sh("dumpsys deviceidle unforce")
        adb.sh("dumpsys battery reset")
        sample("sesudah")
    with (out / "doze.csv").open("w", newline="", encoding="utf-8") as f:
        w = csv.DictWriter(f, fieldnames=["t_s", "phase", "deep", "light", "server"])
        w.writeheader()
        w.writerows(rows)
    dz = [r for r in rows if r["phase"] == "doze"]
    idle = sum(r["deep"] == "IDLE" for r in dz)
    online = sum(r["server"] == "online" for r in dz)
    lines = [f"Doze paksa {a.minutes} menit, {len(dz)} sampel: HP deep IDLE {idle}/{len(dz)}"]
    if api:
        lines.append(f"Server melihat device online {online}/{len(dz)} sampel ({100 * online / max(len(dz), 1):.0f}%)")
    if a.send_to and api:
        lines.append(f"SMS saat Doze: {send_result[0]} setelah {send_result[1]} dtk" if send_result else "SMS saat Doze: BELUM sent/delivered sampai uji selesai (cek dashboard)")
    if idle < len(dz):
        lines.append("PERINGATAN: HP tidak selalu deep IDLE (cek: kabel daya, `dumpsys deviceidle enable deep`, layar menyala)")
    (out / "doze_summary.txt").write_text("\n".join(lines) + "\n", encoding="utf-8")
    print("\n".join(lines))
    return lines


if __name__ == "__main__":
    main()
