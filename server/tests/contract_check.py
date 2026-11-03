#!/usr/bin/env python3
"""Pemeriksaan kepatuhan kontrak: menjalankan biner smscd sungguhan terhadap Postgres sementara, memanggil endpoint,
dan memvalidasi KODE STATUS + BODY setiap respons terhadap skema di contracts/openapi.yaml.

  SMSC_TEST_DATABASE_URL='postgres://nobody@localhost:55432/postgres?sslmode=disable' \
  python3 server/tests/contract_check.py [path-biner-smscd]
Butuh: psql di PATH, pip install pyyaml jsonschema.
"""
import http.cookiejar, json, os, pathlib, re, subprocess, sys, time, urllib.error, urllib.request, uuid

import yaml
from jsonschema import Draft202012Validator, FormatChecker

ROOT = pathlib.Path(__file__).resolve().parents[2]
SPEC = yaml.safe_load((ROOT / "contracts" / "openapi.yaml").read_text())
BIN = sys.argv[1] if len(sys.argv) > 1 else "/tmp/smscd"
BASE = os.environ.get("SMSC_TEST_DATABASE_URL")
if not BASE:
    sys.exit("SMSC_TEST_DATABASE_URL belum diset")

DB = "smsc_ct_" + uuid.uuid4().hex[:8]
DSN = BASE.replace("/postgres?", f"/{DB}?")
PORT = 18000 + int(uuid.uuid4().int % 1000)
URL = f"http://127.0.0.1:{PORT}"


def psql(sql):
    subprocess.run(["psql", BASE, "-qc", sql], check=True, capture_output=True)


# ---------- pencocokan operasi OpenAPI ----------
OPS = []  # (regex, method, path_template, operation)
for tpl, item in SPEC["paths"].items():
    rx = re.compile("^" + re.sub(r"\{[^}]+\}", "[^/]+", tpl) + "$")
    for m, op in item.items():
        if m in ("get", "post", "put", "patch", "delete"):
            OPS.append((rx, m.upper(), tpl, op))


def find_op(method, path):
    p = path.split("?")[0]
    p = p[len("/api/v1"):] if p.startswith("/api/v1") else p
    # path literal (mis. /messages/export) didahulukan atas path ber-parameter
    cands = [o for o in OPS if o[1] == method and o[0].match(p)]
    cands.sort(key=lambda o: o[2].count("{"))
    return cands[0] if cands else None


problems = []
checked = 0


def validate_response(method, path, status, body, content_type):
    global checked
    op = find_op(method, path)
    tag = f"{method} {path} -> {status}"
    if not op:
        problems.append(f"{tag}: operasi tidak ada di openapi.yaml")
        return
    resp = op[3]["responses"].get(str(status))
    if resp is None and status in (401, 403, 429, 500):  # didokumentasikan global di info.description
        resp = SPEC["components"]["responses"].get({401: "Unauthorized", 403: "Forbidden", 429: "TooManyRequests"}.get(status, "Unauthorized"))
    if resp is None:
        problems.append(f"{tag}: status tidak didokumentasikan (ada: {list(op[3]['responses'])})")
        return
    if isinstance(resp, dict) and "$ref" in resp:
        resp = SPEC["components"]["responses"][resp["$ref"].split("/")[-1]]
    content = resp.get("content", {})
    if not content:
        return
    if status >= 400:
        want = "application/problem+json"
    else:
        want = next(iter(content))
    if want not in content:
        problems.append(f"{tag}: content-type {want} tidak didokumentasikan")
        return
    if not content_type.startswith(want):
        problems.append(f"{tag}: content-type {content_type!r}, kontrak {want!r}")
        return
    schema = content[want].get("schema")
    if schema is None or want in ("text/csv", "text/event-stream"):
        return
    root = {"$ref": "#"}  # dummy agar mudah membangun validator
    full = dict(schema)
    full["components"] = SPEC["components"]
    errs = sorted(Draft202012Validator(full, format_checker=FormatChecker()).iter_errors(body), key=lambda e: list(e.path))
    checked += 1
    for e in errs[:3]:
        problems.append(f"{tag}: /{'/'.join(map(str, e.path))}: {e.message[:140]}")


# ---------- klien ----------
jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
csrf = ""


def call(method, path, body=None, expect=None, headers=None, raw=False):
    time.sleep(0.11)  # di bawah batas rate sesi 10 req/dtk
    h = {"Content-Type": "application/json"}
    if csrf and method != "GET":
        h["X-CSRF-Token"] = csrf
    h.update(headers or {})
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(URL + path, data=data, method=method, headers=h)
    try:
        r = opener.open(req, timeout=15)
        status, ct, payload = r.status, r.headers.get("Content-Type", ""), r.read()
    except urllib.error.HTTPError as e:
        status, ct, payload = e.code, e.headers.get("Content-Type", ""), e.read()
    parsed = None
    if payload and ("json" in ct):
        parsed = json.loads(payload)
    if expect is not None and status != expect:
        problems.append(f"{method} {path}: status {status}, diharapkan {expect}: {payload[:200]!r}")
    if raw:
        return status, payload
    validate_response(method, path, status, parsed, ct)
    return status, parsed



def totp_code(secret, offset=0):
    import base64, hashlib, hmac, struct
    key = base64.b32decode(secret + "=" * (-len(secret) % 8))
    d = hmac.new(key, struct.pack(">Q", int(time.time()) // 30 + offset), hashlib.sha1).digest()
    o = d[-1] & 15
    return "%06d" % ((struct.unpack(">I", d[o:o + 4])[0] & 0x7fffffff) % 1000000)


def anon(method, path, body=None, expect=None):
    """Permintaan tanpa cookie (sesi terpisah) yang tetap divalidasi terhadap kontrak."""
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(URL + path, data=data, method=method, headers={"Content-Type": "application/json"})
    try:
        r = urllib.request.build_opener().open(req, timeout=15)
        status, ct, payload = r.status, r.headers.get("Content-Type", ""), r.read()
    except urllib.error.HTTPError as e:
        status, ct, payload = e.code, e.headers.get("Content-Type", ""), e.read()
    parsed = json.loads(payload) if payload and "json" in ct else None
    if expect is not None and status != expect:
        problems.append(f"{method} {path}: status {status}, diharapkan {expect}: {payload[:200]!r}")
    validate_response(method, path, status, parsed, ct)
    return status, parsed



class SoftAuth:
    """Authenticator perangkat lunak (ES256, attestation none) untuk uji kontrak WebAuthn."""
    def __init__(self, rp="example.test", origin="http://example.test"):
        from cryptography.hazmat.primitives.asymmetric import ec
        self.ec, self.key = ec, ec.generate_private_key(ec.SECP256R1())
        self.cred = os.urandom(32); self.count = 0; self.rp = rp; self.origin = origin

    @staticmethod
    def _head(major, n):
        if n < 24: return bytes([major << 5 | n])
        if n < 256: return bytes([major << 5 | 24, n])
        return bytes([major << 5 | 25]) + n.to_bytes(2, "big")

    def _int(self, n): return self._head(0, n) if n >= 0 else self._head(1, -1 - n)
    def _bytes(self, b): return self._head(2, len(b)) + b
    def _text(self, t): return self._head(3, len(t)) + t.encode()
    def _map(self, *kv): return self._head(5, len(kv) // 2) + b"".join(kv)

    def _authdata(self, flags, with_cred):
        import hashlib, struct
        out = hashlib.sha256(self.rp.encode()).digest() + bytes([flags]) + struct.pack(">I", self.count)
        if with_cred:
            nums = self.key.public_key().public_numbers()
            cose = self._map(self._int(1), self._int(2), self._int(3), self._int(-7), self._int(-1), self._int(1),
                             self._int(-2), self._bytes(nums.x.to_bytes(32, "big")), self._int(-3), self._bytes(nums.y.to_bytes(32, "big")))
            out += bytes(16) + len(self.cred).to_bytes(2, "big") + self.cred + cose
        return out

    @staticmethod
    def _b64(b): import base64; return base64.urlsafe_b64encode(b).rstrip(b"=").decode()

    def _cd(self, typ, challenge): return json.dumps({"type": typ, "challenge": challenge, "origin": self.origin, "crossOrigin": False}).encode()

    def create(self, options):
        self.count = 0
        u = options['publicKey'].get('user') or {}
        if u.get('id'):
            import base64; self.handle = base64.urlsafe_b64decode(u['id'] + '=' * (-len(u['id']) % 4))
        att = self._map(self._text("fmt"), self._text("none"), self._text("attStmt"), self._map(), self._text("authData"), self._bytes(self._authdata(0x45, True)))
        return {"id": self._b64(self.cred), "rawId": self._b64(self.cred), "type": "public-key",
                "response": {"clientDataJSON": self._b64(self._cd("webauthn.create", options["publicKey"]["challenge"])), "attestationObject": self._b64(att)},
                "clientExtensionResults": {}}

    def get(self, options):
        import hashlib
        from cryptography.hazmat.primitives import hashes
        self.count += 1
        cd = self._cd("webauthn.get", options["publicKey"]["challenge"]); ad = self._authdata(0x05, False)
        sig = self.key.sign(ad + hashlib.sha256(cd).digest(), self.ec.ECDSA(hashes.SHA256()))
        return {"id": self._b64(self.cred), "rawId": self._b64(self.cred), "type": "public-key",
                "response": {"clientDataJSON": self._b64(cd), "authenticatorData": self._b64(ad), "signature": self._b64(sig), **({"userHandle": self._b64(self.handle)} if getattr(self, "handle", None) else {})},
                "clientExtensionResults": {}}


def main():
    psql(f"CREATE DATABASE {DB}")
    env = dict(os.environ, DATABASE_URL=DSN, PUBLIC_BASE_URL="http://example.test", SESSION_SECRET="s" * 40,
               HTTP_ADDR=f"127.0.0.1:{PORT}", AUTO_MIGRATE="true", SECURE_COOKIES="false", REQUIRE_VPN="false", LOGIN_BURST="60", LOGIN_RATE_PER_MIN="600")
    admin_pw = "kontrak-pagar-biru-12"
    subprocess.run([BIN, "migrate"], env=env, check=True, capture_output=True)
    subprocess.run([BIN, "admin", "create", "--email", "admin@ct.test", "--password", admin_pw], env=env, check=True, capture_output=True)
    srv = subprocess.Popen([BIN, "serve"], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        for _ in range(100):
            try:
                urllib.request.urlopen(URL + "/readyz", timeout=1); break
            except Exception:
                time.sleep(0.1)
        else:
            sys.exit("server tidak siap")
        try:
            run_checks(admin_pw)
        except Exception as e:  # tampilkan ketidaksesuaian yang sudah terkumpul sebelum gagal
            problems.append(f'EXCEPTION: {type(e).__name__}: {e}')
    finally:
        srv.terminate(); srv.wait(timeout=15)
        subprocess.run(["psql", BASE, "-qc", f"DROP DATABASE IF EXISTS {DB} WITH (FORCE)"], capture_output=True)


def run_checks(pw):
    global csrf
    # auth
    call("POST", "/api/v1/auth/login", {"email": "admin@ct.test", "password": "salah"}, 401)
    st, me = call("POST", "/api/v1/auth/login", {"email": "admin@ct.test", "password": pw}, 200)
    csrf = me["csrf_token"]
    call("GET", "/api/v1/auth/me", expect=200)
    call("GET", "/api/v1/devices", expect=200)
    call("GET", "/api/v1/sims", expect=200)

    # enrollment + device
    st, en = call("POST", "/api/v1/device-enrollments", {"device_name": "HP Kontrak"}, 201)
    st, enr = call("POST", "/api/v1/device/enroll", {
        "token": en["token"], "install_id": str(uuid.uuid4()),
        "device_info": {"manufacturer": "Samsung", "model": "A14", "android_version": "13", "sdk_int": 33, "app_version": "poc"},
        "capabilities": {"default_sms_app": False},
        "sims": [{"slot_index": 0, "subscription_id": 3, "carrier_name": "Telkomsel", "registered": True},
                 {"slot_index": 1, "subscription_id": 4, "carrier_name": "XL", "is_esim": True}]}, 201)
    dev = enr["device_id"]
    call("POST", "/api/v1/device/enroll", {"token": "salah", "install_id": str(uuid.uuid4()), "device_info": {"sdk_int": 33}, "sims": []}, 401)
    call("GET", f"/api/v1/devices/{dev}", expect=200)
    call("GET", f"/api/v1/devices/{uuid.uuid4()}", expect=404)
    call("PATCH", f"/api/v1/devices/{dev}", {"name": "HP Kantor"}, 200)
    st, sims = call("GET", f"/api/v1/sims?device_id={dev}", expect=200)
    sim = sims["items"][0]["id"]
    call("GET", f"/api/v1/sims/{sim}", expect=200)
    call("PATCH", f"/api/v1/sims/{sim}", {"label": "Telkomsel Utama"}, 200)
    call("POST", f"/api/v1/devices/{dev}/commands", {"action": "ping"}, 409)  # offline
    st, _ = call("GET", "/api/v1/device/config", headers={"Authorization": "Bearer " + enr["device_secret"]}, expect=200)

    # pesan
    st, created = call("POST", "/api/v1/messages", {"recipients": [{"to": "08123456789", "vars": {}, "client_ref": "ct-1"}, {"to": "xx"}], "body": "Halo dunia"}, 202)
    mid = created["messages"][0]["id"]
    call("POST", "/api/v1/messages", {"recipients": [{"to": "xx"}], "body": "x"}, 422)
    call("GET", "/api/v1/messages?limit=5", expect=200)
    call("GET", "/api/v1/messages?status=queued,failed&sim_id=" + sim, expect=200)
    call("GET", f"/api/v1/messages/{mid}", expect=200)
    call("GET", f"/api/v1/messages/{uuid.uuid4()}", expect=404)
    call("GET", "/api/v1/messages/export", expect=200, raw=True)
    call("POST", f"/api/v1/messages/{mid}/cancel", expect=200)
    call("POST", f"/api/v1/messages/{mid}/cancel", expect=409)
    call("POST", f"/api/v1/messages/{mid}/retry", {}, 409)  # cancelled tidak boleh di-retry

    # template
    st, t = call("POST", "/api/v1/templates", {"name": "tagihan", "body": "Halo {{name}}, {{amount}}"}, 201)
    call("GET", "/api/v1/templates", expect=200)
    call("GET", f"/api/v1/templates/{t['id']}", expect=200)
    call("PUT", f"/api/v1/templates/{t['id']}", {"name": "tagihan", "body": "Halo {{name}}"}, 200)
    call("POST", "/api/v1/templates", {"name": "", "body": ""}, 422)

    # policy
    call("GET", "/api/v1/dispatch-policies", expect=200)
    st, pol = call("POST", "/api/v1/dispatch-policies", {"name": "Promosi", "min_delay_ms": 8000, "max_delay_ms": 20000,
                   "rate_limits": {"per_minute": 4, "per_hour": None, "per_day": 500}}, 201)
    call("GET", f"/api/v1/dispatch-policies/{pol['id']}", expect=200)
    call("PUT", f"/api/v1/dispatch-policies/{pol['id']}", {"name": "Promosi", "min_delay_ms": 9000, "max_delay_ms": 20000}, 200)
    call("GET", f"/api/v1/dispatch-policies/{pol['id']}/effective?sim_id={sim}", expect=200)
    call("POST", "/api/v1/dispatch-policies", {"name": "Salah", "min_delay_ms": 9000, "max_delay_ms": 1000}, 422)
    call("PUT", "/api/v1/settings/default-policy", {"policy_id": str(uuid.uuid4())}, 404)
    call("DELETE", f"/api/v1/dispatch-policies/{pol['id']}", expect=204)

    # admin
    call("POST", "/api/v1/suppressions", {"numbers": ["08111222333"], "reason": "opt-out"}, 201)
    call("GET", "/api/v1/suppressions", expect=200)
    st, k = call("POST", "/api/v1/api-keys", {"name": "billing", "scopes": ["messages:send"]}, 201)
    call("GET", "/api/v1/api-keys", expect=200)
    call("DELETE", f"/api/v1/api-keys/{k['id']}", expect=204)
    call("GET", "/api/v1/users", expect=200)
    call("POST", "/api/v1/users", {"email": "op@ct.test", "role": "operator", "password": "operator-pass-1234"}, 201)
    call("GET", "/api/v1/audit-logs", expect=200)
    call("GET", "/api/v1/stats/overview", expect=200)
    now = time.time()
    iso = lambda t: time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(t))  # noqa: E731
    call("GET", f"/api/v1/stats/timeseries?from={iso(now-86400)}&to={iso(now+3600)}&bucket=hour", expect=200)

    # campaign (fase 2)
    call("GET", "/api/v1/campaigns", expect=200)
    st, cm = call("POST", "/api/v1/campaigns", {"name": "Kontrak", "body": "Halo {{name}}", "priority": 4, "auto_pause": {"window": 20}}, 201, headers={"Idempotency-Key": "ct-1"})
    cid = cm["id"]
    call("POST", "/api/v1/campaigns", {"body": "x"}, 422)
    call("GET", f"/api/v1/campaigns/{cid}", expect=200)
    call("GET", f"/api/v1/campaigns/{uuid.uuid4()}", expect=404)
    call("PATCH", f"/api/v1/campaigns/{cid}", {"description": "uji kontrak", "sim_ids": [sim]}, 200)
    st, imp = call("POST", f"/api/v1/campaigns/{cid}/recipients", {"recipients": [
        {"to": "08123456781", "vars": {"name": "A"}, "client_ref": "r1"}, {"to": "08123456782", "vars": {"name": "B"}},
        {"to": "xx", "vars": {"name": "C"}}, {"to": "08123456781", "vars": {"name": "dup"}}, {"to": "08123456783", "vars": {}}]}, 200)
    call("POST", f"/api/v1/campaigns/{cid}/recipients", {"dedupe": False, "recipients": []}, 422)
    call("POST", f"/api/v1/campaigns/{cid}/preview", {"sample": 2}, 200)
    call("POST", f"/api/v1/campaigns/{uuid.uuid4()}/preview", {}, 404)
    call("POST", f"/api/v1/campaigns/{cid}/pause", {}, 409)  # draft tidak bisa di-pause
    call("POST", f"/api/v1/campaigns/{cid}/start", {}, 200)
    call("PATCH", f"/api/v1/campaigns/{cid}", {"name": "ganti"}, 409)
    call("POST", f"/api/v1/campaigns/{cid}/recipients", {"recipients": []}, 409)
    call("DELETE", f"/api/v1/campaigns/{cid}", expect=409)
    call("POST", f"/api/v1/campaigns/{cid}/pause", {}, 200)
    call("POST", f"/api/v1/campaigns/{cid}/resume", {}, 200)
    call("POST", f"/api/v1/campaigns/{cid}/cancel", {}, 200)
    call("GET", "/api/v1/campaigns?status=cancelled&limit=5", expect=200)
    st, c2 = call("POST", "/api/v1/campaigns", {"name": "Draft"}, 422)
    st, c2 = call("POST", "/api/v1/campaigns", {"name": "Draft", "body": "x"}, 201)
    call("POST", f"/api/v1/campaigns/{c2['id']}/recipients", {"recipients": [{"to": "08123456790"}]}, 200)
    call("DELETE", f"/api/v1/campaigns/{c2['id']}/recipients", expect=200)
    call("DELETE", f"/api/v1/campaigns/{c2['id']}", expect=204)
    # webhook + retensi (fase 3)
    call("GET", "/api/v1/webhooks", expect=200)
    call("POST", "/api/v1/webhooks", {"url": "ftp://x", "events": ["message.sent"]}, 422)
    call("POST", "/api/v1/webhooks", {"url": "http://169.254.169.254/x", "events": ["message.sent"]}, 422)
    st, wh = call("POST", "/api/v1/webhooks", {"url": "http://100.64.0.9:9/hook", "events": ["message.sent", "campaign.completed"], "description": "uji"}, 201)
    wid = wh["id"]
    call("GET", f"/api/v1/webhooks/{wid}", expect=200)
    call("GET", f"/api/v1/webhooks/{uuid.uuid4()}", expect=404)
    call("PATCH", f"/api/v1/webhooks/{wid}", {"description": "ubah", "enabled": True}, 200)
    call("PATCH", f"/api/v1/webhooks/{wid}", {"events": ["bukan.event"]}, 422)
    st, tr = call("POST", f"/api/v1/webhooks/{wid}/test", {}, 202)
    time.sleep(1.5)
    st, dl = call("GET", f"/api/v1/webhooks/{wid}/deliveries", expect=200)
    for d in dl.get("items", [])[:1]:
        call("POST", f"/api/v1/webhooks/{wid}/deliveries/{d['id']}/retry", {}, 409)
    call("POST", f"/api/v1/webhooks/{wid}/deliveries/{uuid.uuid4()}/retry", {}, 404)
    call("DELETE", f"/api/v1/webhooks/{wid}", expect=204)
    st, rt = call("GET", "/api/v1/settings/retention", expect=200)
    call("PUT", "/api/v1/settings/retention", {"body_retention_days": 90, "event_retention_days": 180}, 200)
    call("PUT", "/api/v1/settings/retention", {"body_retention_days": -1, "event_retention_days": 180}, 422)

    # rotasi kredensial + 2FA + ganti password (fase 4)
    st, k2 = call("POST", "/api/v1/api-keys", {"name": "rotasi", "scopes": ["messages:read"]}, 201)
    call("POST", f"/api/v1/api-keys/{k2['id']}/rotate", {"grace_seconds": 60}, 200)
    call("POST", f"/api/v1/api-keys/{k2['id']}/rotate", {"grace_seconds": -5}, 422)
    call("POST", f"/api/v1/api-keys/{uuid.uuid4()}/rotate", {}, 404)
    call("GET", "/api/v1/api-keys", expect=200)
    st, wh2 = call("POST", "/api/v1/webhooks", {"url": "http://100.64.0.9:9/hook", "events": ["message.failed"]}, 201)
    call("POST", f"/api/v1/webhooks/{wh2['id']}/rotate-secret", {"grace_seconds": 60}, 200)
    call("POST", f"/api/v1/webhooks/{wh2['id']}/rotate-secret", {"grace_seconds": 99999999}, 422)
    call("POST", f"/api/v1/webhooks/{uuid.uuid4()}/rotate-secret", {}, 404)
    call("GET", f"/api/v1/webhooks/{wh2['id']}", expect=200)
    call("DELETE", f"/api/v1/webhooks/{wh2['id']}", expect=204)
    call("POST", f"/api/v1/devices/{dev}/commands", {"action": "rotate_credential"}, 409)  # offline

    call("GET", "/api/v1/auth/2fa", expect=200)
    call("POST", "/api/v1/auth/password", {"current_password": "salah-salah-1234", "new_password": "kontrak-baru-12345"}, 401)
    call("POST", "/api/v1/auth/password", {"current_password": pw, "new_password": "pendek"}, 422)
    call("POST", "/api/v1/auth/2fa/setup", {"password": "salah-salah-1234"}, 401)
    st, setup = call("POST", "/api/v1/auth/2fa/setup", {"password": pw}, 200)
    call("POST", "/api/v1/auth/2fa/enable", {"code": "000000"}, 422)
    st, rc = call("POST", "/api/v1/auth/2fa/enable", {"code": totp_code(setup["secret"])}, 200)
    call("POST", "/api/v1/auth/2fa/setup", {"password": pw}, 409)
    call("GET", "/api/v1/auth/2fa", expect=200)
    call("GET", "/api/v1/users", expect=200)
    st, ch = anon("POST", "/api/v1/auth/login", {"email": "admin@ct.test", "password": pw}, 200)
    if not ch or not ch.get("mfa_required"):
        problems.append("login dengan 2FA aktif harus mengembalikan tantangan")
    else:
        anon("POST", "/api/v1/auth/login/verify", {"mfa_token": ch["mfa_token"], "code": "000000"}, 401)
        anon("POST", "/api/v1/auth/login/verify", {"mfa_token": "sampah", "code": "000000"}, 401)
        anon("POST", "/api/v1/auth/login/verify", {"mfa_token": ch["mfa_token"], "code": rc["recovery_codes"][0]}, 200)
    st, rc2 = call("POST", "/api/v1/auth/2fa/recovery-codes", {"password": pw, "code": rc["recovery_codes"][1]}, 200)
    call("POST", "/api/v1/auth/2fa/disable", {"password": pw, "code": "000000"}, 401)
    call("POST", "/api/v1/auth/2fa/disable", {"password": pw, "code": rc2["recovery_codes"][0]}, 204)
    call("POST", "/api/v1/auth/2fa/disable", {"password": pw, "code": "123456"}, 409)
    st, op = call("POST", "/api/v1/users", {"email": "op2@ct.test", "role": "operator", "password": "operator-pass-1234"}, 201)
    call("DELETE", f"/api/v1/users/{op['id']}/2fa", expect=204)
    call("DELETE", f"/api/v1/users/{uuid.uuid4()}/2fa", expect=404)
    call("POST", "/api/v1/auth/password", {"current_password": pw, "new_password": "kontrak-baru-12345"}, 204)

    # manajemen pengguna + kebijakan wajib-2FA (fase 5)
    call("GET", "/api/v1/settings/security", expect=200)
    call("PUT", "/api/v1/settings/security", {"require_2fa": "bukan"}, 422)
    call("PUT", "/api/v1/settings/security", {"require_2fa": "admin"}, 409)  # admin kontrak belum memakai 2FA
    st, nu = call("POST", "/api/v1/users", {"email": "mgmt@ct.test", "role": "viewer", "password": "kuda-melompat-pagar-77"}, 201)
    call("PATCH", f"/api/v1/users/{nu['id']}", {"role": "operator"}, 200)
    call("PATCH", f"/api/v1/users/{nu['id']}", {"disabled": True}, 200)
    call("PATCH", f"/api/v1/users/{nu['id']}", {}, 422)
    call("PATCH", f"/api/v1/users/{nu['id']}", {"role": "dewa"}, 422)
    call("PATCH", f"/api/v1/users/{uuid.uuid4()}", {"role": "viewer"}, 404)
    call("PATCH", f"/api/v1/users/{me['id']}", {"role": "viewer"}, 409)
    call("POST", f"/api/v1/users/{nu['id']}/password", {"new_password": "baru-sekali-12345"}, 204)
    call("POST", f"/api/v1/users/{nu['id']}/password", {"new_password": "pendek"}, 422)
    call("POST", f"/api/v1/users/{me['id']}/password", {"new_password": "baru-sekali-12345"}, 409)
    call("POST", f"/api/v1/users/{uuid.uuid4()}/password", {"new_password": "baru-sekali-12345"}, 404)
    call("GET", "/api/v1/users", expect=200)

    # undangan + hapus pengguna (fase 6)
    call("GET", "/api/v1/invitations", expect=200)
    call("POST", "/api/v1/invitations", {"email": "bukan", "role": "viewer"}, 422)
    call("POST", "/api/v1/invitations", {"email": "mgmt@ct.test", "role": "viewer"}, 409)  # sudah pengguna
    st, inv = call("POST", "/api/v1/invitations", {"email": "tamu@ct.test", "role": "operator", "ttl_hours": 24}, 201)
    call("GET", "/api/v1/invitations", expect=200)
    anon("POST", "/api/v1/invitations/lookup", {"token": inv["token"]}, 200)
    anon("POST", "/api/v1/invitations/lookup", {"token": "inv_salah"}, 404)
    anon("POST", "/api/v1/invitations/accept", {"token": inv["token"], "password": "pendek"}, 422)
    anon("POST", "/api/v1/invitations/accept", {"token": inv["token"], "password": "pagar-biru-melompat-12"}, 201)
    anon("POST", "/api/v1/invitations/accept", {"token": inv["token"], "password": "pagar-biru-melompat-12"}, 404)
    call("DELETE", f"/api/v1/invitations/{inv['id']}", expect=409)
    st, inv2 = call("POST", "/api/v1/invitations", {"email": "batal@ct.test", "role": "viewer"}, 201)
    call("DELETE", f"/api/v1/invitations/{inv2['id']}", expect=204)
    call("DELETE", f"/api/v1/invitations/{uuid.uuid4()}", expect=404)
    call("DELETE", f"/api/v1/users/{nu['id']}", expect=204)  # sudah dinonaktifkan di atas
    call("DELETE", f"/api/v1/users/{nu['id']}", expect=404)
    call("DELETE", f"/api/v1/users/{me['id']}", expect=409)
    st, nu2 = call("POST", "/api/v1/users", {"email": "aktif@ct.test", "role": "viewer", "password": "bola-merah-menggelinding-5"}, 201)
    call("DELETE", f"/api/v1/users/{nu2['id']}", expect=409)  # belum dinonaktifkan

    # kebijakan password + retensi undangan (fase 7)
    call("POST", "/api/v1/users", {"email": "lemah@ct.test", "role": "viewer", "password": "Password123!!"}, 422)
    call("POST", "/api/v1/users", {"email": "lemah@ct.test", "role": "viewer", "password": "lemah-kuda-biru-pagar-9"}, 422)  # memuat nama akun
    call("PUT", "/api/v1/settings/retention", {"body_retention_days": 90, "event_retention_days": 180, "invitation_retention_days": 30}, 200)
    call("PUT", "/api/v1/settings/retention", {"body_retention_days": 90, "event_retention_days": 180, "invitation_retention_days": 0}, 422)
    call("PUT", "/api/v1/settings/retention", {"body_retention_days": 90, "event_retention_days": 180}, 200)
    st, rs = call("GET", "/api/v1/settings/retention", expect=200)
    if rs and rs.get("invitation_retention_days") != 30:
        problems.append("invitation_retention_days harus dipertahankan saat PUT tanpa field itu")

    # email (fase 8) — SMTP tidak dikonfigurasi pada server kontrak
    call("GET", "/api/v1/settings/email", expect=200)
    call("POST", "/api/v1/settings/email/test", {"to": "x@ct.test"}, 409)
    call("POST", "/api/v1/invitations", {"email": "kirim@ct.test", "role": "viewer", "send_email": True}, 409)
    st, inv3 = call("POST", "/api/v1/invitations", {"email": "tanpa-email@ct.test", "role": "viewer", "send_email": False}, 201)
    call("GET", "/api/v1/invitations", expect=200)

    # WebAuthn (fase 9): admin saat ini tanpa 2FA; kunci keamanan menjadi faktor pertama
    pw2 = "kontrak-baru-12345"  # password admin sudah diganti di blok fase 4
    call("GET", "/api/v1/auth/webauthn", expect=200)
    call("POST", "/api/v1/auth/webauthn/register/begin", {"password": "salah-salah-1234"}, 401)
    call("POST", "/api/v1/auth/webauthn/register/finish", {"ceremony_id": str(uuid.uuid4()), "credential": {}}, 400)
    key = SoftAuth()
    st, wb = call("POST", "/api/v1/auth/webauthn/register/begin", {"password": pw2, "name": "Kunci Kontrak"}, 200)
    call("POST", "/api/v1/auth/webauthn/register/finish", {"ceremony_id": wb["ceremony_id"], "credential": {"id": "x"}}, 422)
    st, wb = call("POST", "/api/v1/auth/webauthn/register/begin", {"password": pw2, "name": "Kunci Kontrak"}, 200)
    st, wr = call("POST", "/api/v1/auth/webauthn/register/finish", {"ceremony_id": wb["ceremony_id"], "credential": key.create(wb["options"])}, 201)
    if wr and len(wr.get("recovery_codes", [])) != 10:
        problems.append("kunci pertama harus menerbitkan 10 kode pemulihan")
    call("GET", "/api/v1/auth/webauthn", expect=200)
    call("GET", "/api/v1/auth/2fa", expect=200)
    st, ch = anon("POST", "/api/v1/auth/login", {"email": "admin@ct.test", "password": pw2}, 200)
    if not ch or "webauthn" not in ch.get("methods", []):
        problems.append("tantangan login harus menawarkan webauthn")
    else:
        anon("POST", "/api/v1/auth/login/webauthn/begin", {"mfa_token": "sampah"}, 401)
        st, lb = anon("POST", "/api/v1/auth/login/webauthn/begin", {"mfa_token": ch["mfa_token"]}, 200)
        anon("POST", "/api/v1/auth/login/webauthn/finish", {"mfa_token": ch["mfa_token"], "ceremony_id": str(uuid.uuid4()), "credential": {}}, 400)
        anon("POST", "/api/v1/auth/login/webauthn/finish", {"mfa_token": ch["mfa_token"], "ceremony_id": lb["ceremony_id"], "credential": key.get(lb["options"])}, 200)
    call("DELETE", f"/api/v1/auth/webauthn/credentials/{uuid.uuid4()}", {"password": pw2, "code": wr["recovery_codes"][0]}, 404)
    st, lst = call("GET", "/api/v1/auth/webauthn", expect=200)
    kid = lst["credentials"][0]["id"]
    call("DELETE", f"/api/v1/auth/webauthn/credentials/{kid}", {"password": pw2, "code": "000000"}, 401)
    call("DELETE", f"/api/v1/auth/webauthn/credentials/{kid}", {"password": pw2, "code": wr["recovery_codes"][0]}, 204)

    # passkey (fase 10): default mati; nyalakan lalu daftarkan dan login tanpa password
    anon_get = lambda p: anon("GET", p, None, 200)
    st, mth = anon_get("/api/v1/auth/methods")
    if mth and mth.get("passkey_login") is not False:
        problems.append("passkey_login harus mati secara default")
    call("POST", "/api/v1/auth/webauthn/register/begin", {"password": pw2, "passkey": True}, 409)
    anon("POST", "/api/v1/auth/passkey/begin", {}, 409)
    call("PUT", "/api/v1/settings/security", {}, 422)
    call("PUT", "/api/v1/settings/security", {"passkey_login": True}, 200)
    anon_get("/api/v1/auth/methods")
    pk = SoftAuth()
    st, pb = call("POST", "/api/v1/auth/webauthn/register/begin", {"password": pw2, "name": "Passkey Kontrak", "passkey": True}, 200)
    call("POST", "/api/v1/auth/webauthn/register/finish", {"ceremony_id": pb["ceremony_id"], "credential": pk.create(pb["options"])}, 201)
    st, lb = anon("POST", "/api/v1/auth/passkey/begin", {}, 200)
    anon("POST", "/api/v1/auth/passkey/finish", {"ceremony_id": str(uuid.uuid4()), "credential": {}}, 400)
    anon("POST", "/api/v1/auth/passkey/finish", {"ceremony_id": lb["ceremony_id"], "credential": pk.get(lb["options"])}, 200)
    st, lb = anon("POST", "/api/v1/auth/passkey/begin", {}, 200)
    bad = SoftAuth(); bad.handle = pk.handle
    anon("POST", "/api/v1/auth/passkey/finish", {"ceremony_id": lb["ceremony_id"], "credential": bad.get(lb["options"])}, 401)
    st, lst = call("GET", "/api/v1/auth/webauthn", expect=200)
    for c in lst["credentials"]:
        call("DELETE", f"/api/v1/auth/webauthn/credentials/{c['id']}", {"password": pw2, "code": "000000"}, 401)
    call("PUT", "/api/v1/settings/security", {"passkey_login": False}, 200)

    # revoke di akhir
    call("DELETE", f"/api/v1/devices/{dev}", expect=204)
    call("POST", "/api/v1/auth/logout", expect=204)


if __name__ == "__main__":
    main()
    print(f"{checked} respons divalidasi terhadap openapi.yaml")
    if problems:
        print(f"\n{len(problems)} ketidaksesuaian:")
        for p in dict.fromkeys(problems):
            print(" -", p)
        sys.exit(1)
    print("KONTRAK SESUAI")
