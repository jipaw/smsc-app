#!/usr/bin/env python3
"""E2E agen perangkat JVM (Kotlin :core + OkHttp) terhadap biner smscd sungguhan.

Membuktikan integrasi Kotlin <-> Go tanpa HP: enrollment dari tautan QR, hello/heartbeat, sms.send -> command.ack -> event,
pesan multi-bagian (gagal parsial), gagal sinkron, delivery gagal, rotasi kredensial (disimpan lalu dikonfirmasi),
restart agen dengan job tersimpan, dan sambung ulang memakai secret baru.

Pemakaian: SMSC_TEST_DATABASE_URL=postgres://.../postgres?sslmode=disable python3 agent/e2e.py ../server/bin/smscd agent/build/install/smsc-agent/bin/smsc-agent
"""
import http.cookiejar, json, os, pathlib, subprocess, sys, tempfile, time, urllib.request, uuid

BASE = os.environ.get("SMSC_TEST_DATABASE_URL") or sys.exit("SMSC_TEST_DATABASE_URL belum diset")
SMSCD, AGENT = sys.argv[1], sys.argv[2]
DB = "smsc_agent_" + uuid.uuid4().hex[:8]
PORT = 19000 + uuid.uuid4().int % 900
URL = f"http://127.0.0.1:{PORT}"
env = dict(os.environ, DATABASE_URL=BASE.replace("/postgres?", f"/{DB}?"), PUBLIC_BASE_URL=URL, SESSION_SECRET="s" * 40,
           HTTP_ADDR=f"127.0.0.1:{PORT}", METRICS_ADDR="", AUTO_MIGRATE="true", SECURE_COOKIES="false", REQUIRE_VPN="false",
           DEVICE_ROTATE_GRACE="2s", ALLOWED_DESTINATION_PREFIXES="+62")
psql = lambda q, db=BASE: subprocess.run(["psql", db, "-qAtc", q], check=True, capture_output=True, text=True).stdout.strip()  # noqa: E731
jar = http.cookiejar.CookieJar(); op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar)); csrf = ""
checks = []


def call(method, path, body=None):
    h = {"Content-Type": "application/json"}
    if csrf and method != "GET":
        h["X-CSRF-Token"] = csrf
    r = op.open(urllib.request.Request(URL + path, data=json.dumps(body).encode() if body is not None else None, method=method, headers=h), timeout=30)
    return json.loads(r.read() or b"null")


def ok(cond, what):
    checks.append((bool(cond), what))
    print(("  ✔ " if cond else "  ✘ ") + what, flush=True)
    return cond


def wait(pred, timeout=20, step=0.2):
    end = time.time() + timeout
    while time.time() < end:
        v = pred()
        if v:
            return v
        time.sleep(step)
    return None


def device():
    ds = call("GET", "/api/v1/devices")
    ds = ds.get("items", ds) if isinstance(ds, dict) else ds
    return ds[0] if ds else None


def msg(mid):
    return call("GET", f"/api/v1/messages/{mid}")


def send(to, body):
    r = call("POST", "/api/v1/messages", {"recipients": [{"to": to}], "body": body})
    return r["messages"][0]["id"] if r["messages"] else r


def terminal(mid, states=("delivered", "failed", "sent")):
    return wait(lambda: (m := msg(mid))["status"] in states and m, timeout=30)


def start_agent(state, *extra):
    log = open(os.path.join(state, "agent.log"), "a")
    return subprocess.Popen([AGENT, "--state", state, "--latency-ms", "150", *extra], stdout=log, stderr=subprocess.STDOUT)


def main():
    global csrf
    psql(f"CREATE DATABASE {DB}")
    state = tempfile.mkdtemp(prefix="smsc-agent-")
    srv = agent = None
    try:
        subprocess.run([SMSCD, "migrate"], env=env, check=True, capture_output=True)
        subprocess.run([SMSCD, "admin", "create", "--email", "admin@agent.test", "--password", "agen-pagar-biru-2026"], env=env, check=True, capture_output=True)
        dsn = env["DATABASE_URL"]
        # policy cepat untuk uji (setara harness tes Go): jeda 200 ms, delivery report aktif, jendela sepanjang hari
        psql("""UPDATE dispatch_policies SET min_delay_ms=200, max_delay_ms=200, burst_size=100000, burst_pause_ms=0, rate_per_minute=NULL,
                rate_per_hour=NULL, rate_per_day=NULL, ack_timeout_ms=3000, sent_timeout_ms=4000, track_delivery=true, retry_backoff_ms=1000, retry_backoff_max_ms=2000, max_attempts=2,
                send_window='{"timezone":"UTC","windows":[{"days":[1,2,3,4,5,6,7],"start":"00:00","end":"00:00"}]}' WHERE is_default""", dsn)
        srv = subprocess.Popen([SMSCD, "serve"], env=env, stdout=open(os.path.join(state, "smscd.log"), "w"), stderr=subprocess.STDOUT)
        if not wait(lambda: _ready(), timeout=15):
            sys.exit("smscd tidak siap")
        me = call("POST", "/api/v1/auth/login", {"email": "admin@agent.test", "password": "agen-pagar-biru-2026"}); csrf = me["csrf_token"]

        print("1. enrollment dari tautan QR", flush=True)
        en = call("POST", "/api/v1/device-enrollments", {"device_name": "agen-jvm"})
        ok(en["enroll_url"].startswith("smsc://enroll?"), "server membuat enroll_url (isi QR)")
        agent = start_agent(state, "--enroll", en["enroll_url"])
        d = wait(lambda: (x := device()) and x["status"] == "online" and x, timeout=30)
        ok(d, "agen ter-enroll dan online (hello → hello.ack)")
        creds = json.loads(pathlib.Path(state, "credentials.json").read_text())
        ok(oct(os.stat(pathlib.Path(state, "credentials.json")).st_mode & 0o777) == "0o600", "kredensial disimpan dengan mode 0600")
        sims = call("GET", f"/api/v1/devices/{d['id']}").get("sims") or []
        ok(len(sims) >= 1, f"SIM terlaporkan ke server ({len(sims)})")

        print("2. pengiriman (SmsGateway simulasi)", flush=True)
        a = send("081234560011", "Halo dari agen JVM")
        b = send("081234560000", "gagal sinkron")
        c = send("081234560002", "delivery gagal")
        long_body = "Pesan panjang multi-bagian dari agen. " * 6   # > 160 karakter GSM → 2 bagian
        p = send("081234560001", long_body)
        r = call("POST", "/api/v1/messages", {"recipients": [{"to": "+14155550123"}, {"to": "081234560012"}], "body": "campuran"})
        ok(r["rejected"] and r["rejected"][0]["reason"] == "destination_not_allowed", "tujuan di luar ALLOWED_DESTINATION_PREFIXES ditolak server")
        ma = terminal(a, ("delivered", "failed"))
        ok(ma and ma["status"] == "delivered", f"pesan normal → delivered ({ma and ma['status']})")
        mb = terminal(b, ("failed",))
        ok(mb and mb.get("error_code") == "generic_failure" and mb.get("attempt") == 2,
           f"gagal sinkron (retryable) → dicoba ulang lalu failed/generic_failure setelah max_attempts ({mb and mb.get('error_code')}, attempt={mb and mb.get('attempt')})")
        mc = terminal(c, ("failed",))
        ok(mc and mc.get("error_code") == "delivery_failed", f"laporan delivery gagal → failed/delivery_failed ({mc and mc.get('error_code')})")
        mp = terminal(p, ("failed",))
        ok(mp and mp["parts"] == 2 and mp.get("partial") is True, f"multi-bagian: bagian 1 terkirim, bagian 2 gagal → partial=true (parts={mp and mp['parts']}, partial={mp and mp.get('partial')})")
        ok(mp and mp.get("attempt", 1) == 1, "gagal parsial tidak diantre ulang (attempt tetap 1)")

        print("3. rotasi kredensial", flush=True)
        call("POST", f"/api/v1/devices/{d['id']}/commands", {"action": "rotate_credential"})
        rotated = wait(lambda: json.loads(pathlib.Path(state, "credentials.json").read_text())["deviceSecret"] != creds["deviceSecret"], timeout=15)
        ok(rotated, "agen menyimpan secret baru lalu membalas ok")
        ok(wait(lambda: psql(f"SELECT pending_secret_hash IS NULL AND rotated_at IS NOT NULL FROM device_credentials WHERE device_id='{d['id']}'", dsn) == "t", timeout=10),
           "server mempromosikan secret baru (pending dikosongkan)")

        print("4. restart agen: job & kredensial tersimpan, sambung ulang dengan secret baru", flush=True)
        agent.terminate(); agent.wait(timeout=15)
        ok(wait(lambda: device()["status"] != "online", timeout=15), "server mendeteksi agen terputus")
        time.sleep(2.5)  # lewati masa grace secret lama: hanya secret baru yang berlaku
        agent = start_agent(state)
        ok(wait(lambda: device()["status"] == "online", timeout=30), "agen online kembali memakai secret hasil rotasi")
        e = send("081234560099", "setelah restart")
        me2 = terminal(e, ("delivered", "failed"))
        ok(me2 and me2["status"] == "delivered", f"pengiriman setelah restart → delivered ({me2 and me2['status']})")
        jobs = json.loads(pathlib.Path(state, "jobs.json").read_text())
        ok(len(jobs) >= 5, f"job tersimpan persisten di agen ({len(jobs)})")

        print("5. kredensial lama ditolak", flush=True)
        old = subprocess.run([AGENT, "--state", _old_state(creds), "--latency-ms", "150"], capture_output=True, text=True, timeout=30)
        ok(old.returncode == 3 and "kredensial ditolak (401)" in old.stdout, "agen dengan secret lama ditolak (HTTP 401 saat upgrade) dan berhenti, tidak mencoba ulang")
    finally:
        for p in (agent, srv):
            if p and p.poll() is None:
                p.terminate()
                try:
                    p.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    p.kill()
        subprocess.run(["psql", BASE, "-qc", f"DROP DATABASE IF EXISTS {DB} WITH (FORCE)"], capture_output=True)
    bad = [w for good, w in checks if not good]
    print(f"\n{len(checks) - len(bad)}/{len(checks)} pemeriksaan lulus" + (f"; log: {state}" if bad else ""))
    sys.exit(1 if bad else 0)


def _old_state(creds):
    d = tempfile.mkdtemp(prefix="smsc-agent-old-")
    pathlib.Path(d, "credentials.json").write_text(json.dumps(creds))
    return d


def _ready():
    try:
        return urllib.request.urlopen(URL + "/readyz", timeout=1).status == 200
    except Exception:
        return False


if __name__ == "__main__":
    main()
