#!/usr/bin/env python3
"""Kumpulkan bukti uji PoC dari HP lewat adb dalam satu perintah (Windows: `py poc\\tools\\collect.py`).

Hasil: poc/hasil/<merek>-<model>-<waktu>[-label]/
  files/        berkas aplikasi (poc.log, ledger.csv, events_*.csv) dari /sdcard/Android/data/id.smsc.poc/files/
  device.json   info perangkat & status aplikasi (izin, pengecualian baterai, standby bucket, peran SMS, setelan batas SMS OS)
  logcat.txt    (opsional --logcat) log tag SMSC-POC
  summary.md    ringkasan siap tempel ke RESULTS_TEMPLATE.md (termasuk keluaran analyze.py)

Hanya stdlib Python 3.8+. Butuh adb di PATH (Android SDK platform-tools) dan USB debugging aktif.
"""
import argparse
import datetime as dt
import json
import pathlib
import re
import shutil
import subprocess
import sys

PKG = "id.smsc.poc"
FILES = f"/sdcard/Android/data/{PKG}/files/"
PERMS = ["SEND_SMS", "READ_PHONE_STATE", "READ_PHONE_NUMBERS", "POST_NOTIFICATIONS", "CAMERA"]

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import analyze  # noqa: E402


class Adb:
    def __init__(self, serial=None):
        self.serial = serial
        self.adb = shutil.which("adb")
        if not self.adb:
            sys.exit("adb tidak ditemukan di PATH (pasang Android SDK platform-tools)")

    def run(self, *args, check=False, timeout=60):
        cmd = [self.adb] + (["-s", self.serial] if self.serial else []) + list(args)
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, encoding="utf-8", errors="replace")
        if check and r.returncode != 0:
            raise RuntimeError(f"adb {' '.join(args)} gagal: {r.stderr.strip() or r.stdout.strip()}")
        return r.stdout

    def sh(self, cmd):
        return self.run("shell", cmd).strip()


def pick_device(adb, serial):
    out = adb.run("devices")
    devs = [l.split("\t")[0] for l in out.splitlines()[1:] if l.strip().endswith("\tdevice")]
    unauthorized = [l.split("\t")[0] for l in out.splitlines()[1:] if "unauthorized" in l]
    if serial:
        if serial not in devs:
            sys.exit(f"perangkat {serial} tidak tersambung/terotorisasi. Terlihat: {devs or 'tidak ada'}")
        return serial
    if unauthorized and not devs:
        sys.exit("HP belum mengizinkan USB debugging: buka layar HP dan setujui sidik jari RSA, lalu ulangi")
    if not devs:
        sys.exit("tidak ada HP tersambung (adb devices kosong). Aktifkan Opsi pengembang → USB debugging")
    if len(devs) > 1:
        sys.exit(f"lebih dari satu HP tersambung: {devs}. Pilih dengan --serial")
    return devs[0]


def package_info(dump):
    info = {"installed": "versionName=" in dump}
    m = re.search(r"versionName=(\S+)", dump)
    info["version"] = m.group(1) if m else None
    m = re.search(r"installerPackageName=(\S+)", dump)
    info["installer"] = m.group(1) if m else None  # null = adb install / sideload (R2)
    m = re.search(r"firstInstallTime=([^\n]+)", dump)
    info["first_install"] = m.group(1).strip() if m else None
    granted = {}
    for p in PERMS:
        m = re.search(rf"android\.permission\.{p}: granted=(true|false)", dump)
        granted[p] = (m.group(1) == "true") if m else None
    info["permissions"] = granted
    return info


def collect(adb, out_dir, logcat):
    d = {}
    for k, prop in {"manufacturer": "ro.product.manufacturer", "model": "ro.product.model", "android": "ro.build.version.release",
                    "sdk": "ro.build.version.sdk", "fingerprint": "ro.build.fingerprint"}.items():
        d[k] = adb.sh(f"getprop {prop}")
    d["app"] = package_info(adb.sh(f"dumpsys package {PKG}"))
    d["battery_exempt"] = PKG in adb.sh("dumpsys deviceidle whitelist")
    d["standby_bucket"] = adb.sh(f"am get-standby-bucket {PKG}")
    d["sms_role_holder"] = adb.sh("cmd role get-role-holders android.app.role.SMS")
    d["sms_outgoing_check_max_count"] = adb.sh("settings get global sms_outgoing_check_max_count")
    d["sms_outgoing_check_interval_ms"] = adb.sh("settings get global sms_outgoing_check_interval_ms")
    bat = adb.sh("dumpsys battery")
    m = re.search(r"level: (\d+)", bat)
    d["battery_level"] = int(m.group(1)) if m else None
    d["charging"] = bool(re.search(r"(AC|USB|Wireless) powered: true", bat))
    d["service_running"] = "PocService" in adb.sh(f"dumpsys activity services {PKG}")
    d["collected_at"] = dt.datetime.now().isoformat(timespec="seconds")
    (out_dir / "device.json").write_text(json.dumps(d, indent=2, ensure_ascii=False), encoding="utf-8")

    files = out_dir / "files"
    files.mkdir(parents=True, exist_ok=True)
    pull = adb.run("pull", FILES, str(files), timeout=600)
    # `adb pull <dir>/ <tujuan>` membuat subfolder "files"; ratakan agar analyze.py langsung menemukan berkasnya
    nested = files / "files"
    if nested.is_dir():
        for p in nested.iterdir():
            p.replace(files / p.name)
        nested.rmdir()
    if logcat:
        (out_dir / "logcat.txt").write_text(adb.run("logcat", "-d", "-v", "time", "-s", "SMSC-POC:I", timeout=120), encoding="utf-8")
    return d, pull


def summary(d, out_dir):
    a = d["app"]
    rows = [
        ("Perangkat", f"{d['manufacturer']} {d['model']} — Android {d['android']} (API {d['sdk']})"),
        ("Aplikasi", f"{a['version'] or 'TIDAK TERPASANG'} · installer: {a['installer'] or 'null (adb/sideload)'} · dipasang {a['first_install'] or '-'}"),
        ("Izin", ", ".join(f"{k}={'ya' if v else ('tidak' if v is False else '?')}" for k, v in a["permissions"].items())),
        ("Pengecualian optimasi baterai", "ya" if d["battery_exempt"] else "TIDAK (R3: koneksi bisa diputus Doze)"),
        ("Standby bucket", d["standby_bucket"]),
        ("Aplikasi SMS default", d["sms_role_holder"] or "-"),
        ("sms_outgoing_check_max_count / interval_ms", f"{d['sms_outgoing_check_max_count']} / {d['sms_outgoing_check_interval_ms']}"),
        ("Baterai", f"{d['battery_level']}% {'(mengisi)' if d['charging'] else '(tidak mengisi)'}"),
        ("Service berjalan", "ya" if d["service_running"] else "tidak"),
    ]
    lines = [f"# Ringkasan uji — {out_dir.name}", "", "| Butir | Nilai |", "|---|---|"] + [f"| {k} | {v} |" for k, v in rows]
    files = out_dir / "files"
    an = []
    for p in sorted(files.glob("ledger.csv")):
        an.append(analyze.analyze_ledger(p))
    for p in sorted(files.glob("events_*.csv")):
        an.append(analyze.analyze_events(p))
    lines += ["", "## analyze.py", "", "```", *(an or ["(tidak ada ledger.csv/events_*.csv — service/burst belum dijalankan?)"]), "```"]
    present = sorted(p.name for p in files.iterdir()) if files.is_dir() else []
    lines += ["", f"Berkas: {', '.join(present) or '-'}"]
    (out_dir / "summary.md").write_text("\n".join(lines) + "\n", encoding="utf-8")
    return "\n".join(lines)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--serial", help="pilih HP bila lebih dari satu tersambung (lihat `adb devices`)")
    ap.add_argument("--out", default=str(pathlib.Path(__file__).resolve().parents[1] / "hasil"), help="folder induk hasil (default poc/hasil)")
    ap.add_argument("--label", default="", help="label uji, mis. idle-8j atau r1-skenario-a")
    ap.add_argument("--logcat", action="store_true", help="ikut simpan logcat tag SMSC-POC")
    a = ap.parse_args(argv)
    adb = Adb()
    adb.serial = pick_device(adb, a.serial)
    man = re.sub(r"[^a-z0-9]+", "-", adb.sh("getprop ro.product.manufacturer").lower()).strip("-") or "hp"
    model = re.sub(r"[^a-z0-9]+", "-", adb.sh("getprop ro.product.model").lower()).strip("-") or "x"
    name = f"{man}-{model}-{dt.datetime.now():%Y%m%d-%H%M}" + (f"-{re.sub(r'[^A-Za-z0-9_-]+', '-', a.label)}" if a.label else "")
    out_dir = pathlib.Path(a.out) / name
    out_dir.mkdir(parents=True, exist_ok=True)
    d, _ = collect(adb, out_dir, a.logcat)
    if not d["app"]["installed"]:
        print(f"PERINGATAN: paket {PKG} tidak terpasang di HP ini")
    print(summary(d, out_dir))
    print(f"\nTersimpan di {out_dir}")
    return out_dir


if __name__ == "__main__":
    main()
