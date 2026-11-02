#!/usr/bin/env python3
"""Uji beban campaign: impor 50.000 penerima (CSV), start, amati materialisasi (batas antrean 20.000) dan latensi API.
Tanpa HP online (pesan tetap 'queued'), jadi yang diukur adalah jalur data server + PostgreSQL.
  SMSC_TEST_DATABASE_URL=... python3 server/tests/load_campaign.py [biner] [jumlah=50000]"""
import http.cookiejar, io, json, os, subprocess, sys, time, urllib.request, uuid, statistics

BIN = sys.argv[1] if len(sys.argv) > 1 else "/tmp/smscd"
N = int(sys.argv[2]) if len(sys.argv) > 2 else 50000
BASE = os.environ["SMSC_TEST_DATABASE_URL"]
DB = "smsc_load_" + uuid.uuid4().hex[:8]
PORT = 17000 + uuid.uuid4().int % 900
URL = f"http://127.0.0.1:{PORT}"
env = dict(os.environ, DATABASE_URL=BASE.replace("/postgres?", f"/{DB}?"), PUBLIC_BASE_URL="http://x.test", SESSION_SECRET="s" * 40,
           HTTP_ADDR=f"127.0.0.1:{PORT}", AUTO_MIGRATE="true", SECURE_COOKIES="false", REQUIRE_VPN="false", CAMPAIGN_TICK="500ms")
psql = lambda q: subprocess.run(["psql", BASE, "-qc", q], check=True, capture_output=True)  # noqa: E731
jar = http.cookiejar.CookieJar(); op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar)); csrf = ""


def call(method, path, body=None, raw=None, ctype="application/json"):
    h = {"Content-Type": ctype}
    if csrf and method != "GET": h["X-CSRF-Token"] = csrf
    data = raw if raw is not None else (json.dumps(body).encode() if body is not None else None)
    t = time.time()
    r = op.open(urllib.request.Request(URL + path, data=data, method=method, headers=h), timeout=120)
    out = json.loads(r.read() or b"null")
    return out, (time.time() - t) * 1000


psql(f"CREATE DATABASE {DB}")
try:
    subprocess.run([BIN, "migrate"], env=env, check=True, capture_output=True)
    subprocess.run([BIN, "admin", "create", "--email", "a@l.test", "--password", "load-test-password-1"], env=env, check=True, capture_output=True)
    srv = subprocess.Popen([BIN, "serve"], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    for _ in range(100):
        try: urllib.request.urlopen(URL + "/readyz", timeout=1); break
        except Exception: time.sleep(0.1)
    me, _ = call("POST", "/api/v1/auth/login", {"email": "a@l.test", "password": "load-test-password-1"}); csrf = me["csrf_token"]
    # SIM harus ada (tidak perlu online): daftarkan device lewat enrollment
    en, _ = call("POST", "/api/v1/device-enrollments", {})
    call("POST", "/api/v1/device/enroll", {"token": en["token"], "install_id": str(uuid.uuid4()), "device_info": {"sdk_int": 33, "manufacturer": "x", "model": "y", "app_version": "1"},
         "sims": [{"slot_index": 0, "subscription_id": 1}]})
    cm, _ = call("POST", "/api/v1/campaigns", {"name": "Beban", "body": "Halo {{name}}, tagihan {{amount}}"})
    rows = ["phone,name,amount"] + [f"0812{1000000 + i:07d},Nama{i},{i}" for i in range(N)]
    csv = "\n".join(rows).encode()
    boundary = "----load" + uuid.uuid4().hex
    body = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"p.csv\"\r\nContent-Type: text/csv\r\n\r\n").encode() + csv + f"\r\n--{boundary}--\r\n".encode()
    res, ms = call("POST", f"/api/v1/campaigns/{cm['id']}/recipients", raw=body, ctype=f"multipart/form-data; boundary={boundary}")
    print(f"impor CSV {len(csv)/1e6:.1f} MB, {res['accepted']} diterima: {ms/1000:.1f} dtk ({res['accepted']/(ms/1000):,.0f} baris/dtk)")
    assert res["accepted"] == N, res
    _, ms = call("POST", f"/api/v1/campaigns/{cm['id']}/preview", {}); print(f"preview: {ms:.0f} ms")
    _, ms = call("POST", f"/api/v1/campaigns/{cm['id']}/start", {}); print(f"start (validasi {N} penerima): {ms:.0f} ms")
    t0 = time.time(); last = -1; lat = []
    while time.time() - t0 < 90:
        c, ms = call("GET", f"/api/v1/campaigns/{cm['id']}"); lat.append(ms)
        q = c["counters"]["queued"] - (c["counters"]["total"] - 0 if False else 0)
        mat = int(subprocess.run(["psql", env["DATABASE_URL"], "-tAc", f"select count(*) from messages where campaign_id='{cm['id']}'"], capture_output=True, text=True).stdout.strip() or 0)
        if mat != last: print(f"  t={time.time()-t0:5.1f}s  materialisasi={mat:6d}  GET campaign {ms:5.0f} ms"); last = mat
        if mat >= min(N, 20000) and time.time() - t0 > 25: break
        time.sleep(1)
    time.sleep(3)
    mat2 = int(subprocess.run(["psql", env["DATABASE_URL"], "-tAc", f"select count(*) from messages where campaign_id='{cm['id']}'"], capture_output=True, text=True).stdout.strip())
    page_ms = []
    for _ in range(5):
        _, ms = call("GET", f"/api/v1/messages?limit=50&campaign_id={cm['id']}"); page_ms.append(ms)
    c, ms = call("GET", f"/api/v1/campaigns/{cm['id']}")
    k = c["counters"]
    print(f"\nmaterialisasi berhenti di {mat2} (batas antrean 20.000)  counters: total={k['total']} queued={k['queued']}  invarian={'OK' if sum(k[x] for x in ['queued','in_flight','sent','delivered','failed','cancelled','expired','unknown']) == k['total'] else 'RUSAK'}")
    print(f"GET campaign (n={len(lat)}): median {statistics.median(lat):.0f} ms, maks {max(lat):.0f} ms")
    print(f"GET /messages?campaign_id (halaman 50): median {statistics.median(page_ms):.0f} ms, maks {max(page_ms):.0f} ms")
    _, ms = call("POST", f"/api/v1/campaigns/{cm['id']}/cancel", {}); print(f"cancel (batalkan {k['queued']} antrean): {ms:.0f} ms")
    c, _ = call("GET", f"/api/v1/campaigns/{cm['id']}"); k = c["counters"]
    print(f"setelah cancel: cancelled={k['cancelled']} queued={k['queued']} total={k['total']}")
finally:
    try: srv.terminate(); srv.wait(timeout=10)
    except Exception: pass
    subprocess.run(["psql", BASE, "-qc", f"DROP DATABASE IF EXISTS {DB} WITH (FORCE)"], capture_output=True)
