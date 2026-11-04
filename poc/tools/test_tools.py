#!/usr/bin/env python3
"""Tes collect.py dan doze.py dengan adb palsu (skrip di PATH) dan server smscd palsu. Linux/macOS: python3 poc/tools/test_tools.py"""
import contextlib
import io
import json
import os
import pathlib
import stat
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import collect  # noqa: E402
import doze  # noqa: E402

FAKE_ADB = r'''#!/usr/bin/env python3
import os, sys, shutil, pathlib
log = pathlib.Path(os.environ["FAKE_ADB_LOG"]); state = pathlib.Path(os.environ["FAKE_ADB_STATE"])
a = sys.argv[1:]
if a[:1] == ["-s"]: a = a[2:]
with log.open("a") as f: f.write(" ".join(a) + "\n")
if a == ["devices"]:
    print("List of devices attached"); print(os.environ.get("FAKE_DEVICES", "R5CT123\tdevice")); sys.exit(0)
if a[0] == "pull":
    dst = pathlib.Path(a[2]) / "files"; shutil.copytree(os.environ["FAKE_FILES"], dst); print("3 files pulled"); sys.exit(0)
if a[0] == "logcat":
    print("11-04 10:00:00.000 I/SMSC-POC: [ws] online"); sys.exit(0)
cmd = " ".join(a[1:])
out = {
  "getprop ro.product.manufacturer": "samsung", "getprop ro.product.model": "SM-A145F",
  "getprop ro.build.version.release": "14", "getprop ro.build.version.sdk": "34", "getprop ro.build.fingerprint": "samsung/x",
  "dumpsys package id.smsc.poc": "    versionName=poc-0.1\n    firstInstallTime=2026-11-04 09:00:00\n    installerPackageName=null\n"
     "      android.permission.SEND_SMS: granted=true\n      android.permission.READ_PHONE_STATE: granted=true\n"
     "      android.permission.CAMERA: granted=false\n",
  "dumpsys deviceidle whitelist": "user,id.smsc.poc,10234\nsystem,com.android.phone,1001",
  "am get-standby-bucket id.smsc.poc": "10",
  "cmd role get-role-holders android.app.role.SMS": "com.google.android.apps.messaging",
  "settings get global sms_outgoing_check_max_count": "30", "settings get global sms_outgoing_check_interval_ms": "1800000",
  "dumpsys battery": "  AC powered: false\n  USB powered: true\n  level: 87",
  "dumpsys activity services id.smsc.poc": "  * ServiceRecord{1 u0 id.smsc.poc/.PocService}",
}
if cmd == "dumpsys deviceidle force-idle": state.write_text("IDLE"); print("Now forced in to deep idle mode"); sys.exit(0)
if cmd == "dumpsys deviceidle unforce": state.write_text("ACTIVE"); sys.exit(0)
if cmd in ("dumpsys deviceidle get deep", "dumpsys deviceidle get light"):
    print(state.read_text() if state.exists() else "ACTIVE"); sys.exit(0)
print(out.get(cmd, ""))
'''


class Env(contextlib.ExitStack):
    def __init__(self, devices="R5CT123\tdevice"):
        super().__init__()
        self.tmp = pathlib.Path(self.enter_context(tempfile.TemporaryDirectory()))
        bindir = self.tmp / "bin"
        bindir.mkdir()
        adb = bindir / "adb"
        adb.write_text(FAKE_ADB)
        adb.chmod(adb.stat().st_mode | stat.S_IEXEC)
        files = self.tmp / "fixture"
        files.mkdir()
        (files / "ledger.csv").write_text("ts_ms,elapsed_s,event,detail\n1000,0,start,x\n2000,1,online,s\n3602000,3601,disconnected,\"code=1001 reason=closing:x uptime=3600000ms\"\n3605000,3604,online,s\n7202000,7201,stop,x\n")
        (files / "events_a.csv").write_text("ts_ms,at,message_id,attempt,state,part_index,parts_total,error_code,android_result_code\n"
                                            "1000,x,m1,1,sending,,1,,\n2000,x,m1,1,sent,,1,,-1\n5000,x,m1,1,delivered,,1,,0\n")
        (files / "poc.log").write_text("log\n")
        self.log = self.tmp / "adb.log"
        self.env = {"PATH": f"{bindir}{os.pathsep}{os.environ['PATH']}", "FAKE_ADB_LOG": str(self.log),
                    "FAKE_ADB_STATE": str(self.tmp / "idle"), "FAKE_FILES": str(files), "FAKE_DEVICES": devices}

    def __enter__(self):
        super().__enter__()
        self._old = {k: os.environ.get(k) for k in self.env}
        os.environ.update(self.env)
        self.callback(self._restore)
        return self

    def _restore(self):
        for k, v in self._old.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v

    def calls(self):
        return self.log.read_text().splitlines() if self.log.exists() else []


class CollectTest(unittest.TestCase):
    def test_collects_and_summarizes(self):
        with Env() as e, contextlib.redirect_stdout(io.StringIO()):
            out = collect.main(["--out", str(e.tmp / "hasil"), "--label", "idle 8j", "--logcat"])
            self.assertTrue(out.name.startswith("samsung-sm-a145f-") and out.name.endswith("-idle-8j"), out.name)
            d = json.loads((out / "device.json").read_text())
            self.assertEqual(d["android"], "14")
            self.assertIsNone(d["app"]["installer"] if d["app"]["installer"] != "null" else None)
            self.assertEqual(d["app"]["permissions"]["SEND_SMS"], True)
            self.assertEqual(d["app"]["permissions"]["CAMERA"], False)
            self.assertIsNone(d["app"]["permissions"]["POST_NOTIFICATIONS"])
            self.assertTrue(d["battery_exempt"] and d["service_running"] and d["charging"])
            self.assertEqual(d["sms_outgoing_check_max_count"], "30")
            self.assertTrue((out / "files" / "ledger.csv").exists() and not (out / "files" / "files").exists(), "berkas diratakan")
            s = (out / "summary.md").read_text()
            for want in ["SM-A145F", "Android 14 (API 34)", "SEND_SMS=ya", "CAMERA=tidak", "30 / 1800000", "87% (mengisi)",
                         "ledger.csv: durasi=", "putus=1", "events_a.csv: pesan=1 sent=1 delivered=1"]:
                self.assertIn(want, s)
            self.assertIn("online", (out / "logcat.txt").read_text())

    def test_device_selection_errors(self):
        for devices, msg in [("", "tidak ada HP"), ("A\tdevice\nB\tdevice", "lebih dari satu"), ("A\tunauthorized", "USB debugging")]:
            with Env(devices) as e, self.assertRaises(SystemExit) as cm:
                collect.main(["--out", str(e.tmp)])
            self.assertIn(msg, str(cm.exception.code))
        with Env("A\tdevice\nB\tdevice") as e, contextlib.redirect_stdout(io.StringIO()):
            out = collect.main(["--out", str(e.tmp), "--serial", "B"])
            self.assertTrue(all(c.startswith(("devices", "shell", "pull", "logcat")) for c in e.calls()))
            self.assertTrue(out.exists())


class FakeServer:
    def __init__(self, fail_messages=False):
        self.calls, self.polls, self.fail = [], 0, fail_messages
        outer = self

        class H(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def _send(self, code, obj):
                b = json.dumps(obj).encode()
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(b)))
                if self.path == "/api/v1/auth/login":
                    self.send_header("Set-Cookie", "sid=1; Path=/")
                self.end_headers()
                self.wfile.write(b)

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                outer.calls.append((self.path, body, self.headers.get("X-CSRF-Token")))
                if self.path == "/api/v1/auth/login":
                    return self._send(200 if body["password"] == "rahasia" else 401, {"csrf_token": "c1"})
                if outer.fail:
                    return self._send(500, {"title": "rusak"})
                self._send(202, {"messages": [{"id": "msg1"}], "rejected": []})

            def do_GET(self):
                if self.path == "/api/v1/devices":
                    return self._send(200, {"items": [{"id": "d1", "status": "online"}]})
                outer.polls += 1
                self._send(200, {"status": "sent" if outer.polls >= 2 else "assigned"})

        self.srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        self.url = f"http://127.0.0.1:{self.srv.server_address[1]}"


class DozeTest(unittest.TestCase):
    def test_forced_doze_cycle_with_server_and_send(self):
        srv = FakeServer()
        with Env() as e, contextlib.redirect_stdout(io.StringIO()):
            os.environ["SMSC_PASSWORD"] = "rahasia"
            lines = doze.main(["--minutes", "0.05", "--interval", "0.3", "--server", srv.url, "--email", "a@b.c", "--send-to", "0812", "--send-at", "0", "--out", str(e.tmp)])
            calls = e.calls()
        os.environ.pop("SMSC_PASSWORD")
        srv.srv.shutdown(); srv.srv.server_close()
        order = [c for c in calls if c in ("shell dumpsys battery unplug", "shell dumpsys deviceidle force-idle", "shell dumpsys deviceidle unforce", "shell dumpsys battery reset")]
        self.assertEqual(order, ["shell dumpsys battery unplug", "shell dumpsys deviceidle force-idle", "shell dumpsys deviceidle unforce", "shell dumpsys battery reset"])
        self.assertIn("HP deep IDLE", lines[0])
        self.assertTrue(lines[0].endswith(tuple(f"{n}/{n}" for n in range(1, 20))), lines[0])
        self.assertIn("online", lines[1])
        self.assertTrue(any(l.startswith("SMS saat Doze: sent setelah") for l in lines), lines)
        posts = [c for c in srv.calls if c[0] == "/api/v1/messages"]
        self.assertEqual(posts[0][2], "c1", "CSRF dikirim")
        self.assertEqual(posts[0][1]["recipients"], [{"to": "0812"}])

    def test_recovery_runs_even_when_api_fails(self):
        srv = FakeServer(fail_messages=True)
        with Env() as e, contextlib.redirect_stdout(io.StringIO()):
            os.environ["SMSC_PASSWORD"] = "rahasia"
            with self.assertRaises(RuntimeError):
                doze.main(["--minutes", "0.05", "--interval", "0.3", "--server", srv.url, "--email", "a@b.c", "--send-to", "0812", "--send-at", "0", "--out", str(e.tmp)])
            calls = e.calls()
        os.environ.pop("SMSC_PASSWORD")
        srv.srv.shutdown(); srv.srv.server_close()
        # pemulihan dijalankan walau API gagal di tengah uji: unforce lalu battery reset, sesudah force-idle
        i_force, i_unforce, i_reset = (calls.index(c) for c in ("shell dumpsys deviceidle force-idle", "shell dumpsys deviceidle unforce", "shell dumpsys battery reset"))
        self.assertLess(i_force, i_unforce)
        self.assertLess(i_unforce, i_reset)

    def test_without_server(self):
        with Env() as e, contextlib.redirect_stdout(io.StringIO()):
            lines = doze.main(["--minutes", "0.02", "--interval", "0.3", "--out", str(e.tmp)])
            self.assertTrue((e.tmp / "doze.csv").exists() and (e.tmp / "doze_summary.txt").exists())
        self.assertEqual(len(lines), 1)


if __name__ == "__main__":
    unittest.main(verbosity=2)
