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
    if resp is None and status == 501 and (path.startswith('/api/v1/campaigns') or path.startswith('/api/v1/webhooks')):
        return  # fase 2/3, didokumentasikan global
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


def main():
    psql(f"CREATE DATABASE {DB}")
    env = dict(os.environ, DATABASE_URL=DSN, PUBLIC_BASE_URL="http://example.test", SESSION_SECRET="s" * 40,
               HTTP_ADDR=f"127.0.0.1:{PORT}", AUTO_MIGRATE="true", SECURE_COOKIES="false", REQUIRE_VPN="false")
    admin_pw = "kontrak-admin-12345"
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

    # fase berikutnya: kontrak ada, implementasi 501 (didokumentasikan?)
    st, _ = call("GET", "/api/v1/campaigns")
    if st != 501:
        problems.append(f"GET /campaigns harus 501 sementara, dapat {st}")

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
