#!/usr/bin/env python3
"""Ringkas hasil PoC dari berkas yang di-pull dari HP.

  adb pull /sdcard/Android/data/id.smsc.poc/files/ poc/hasil/
  python3 poc/tools/analyze.py poc/hasil/files

- ledger.csv  (R3): fraksi waktu ONLINE, jumlah putus, daftar putus beserta penyebabnya.
- events_*.csv (R1/R5): jeda antar 'sending', latensi sending->sent, sent->delivered, histogram error, macet.
"""
import csv
import pathlib
import statistics as stat
import sys
from collections import Counter


def fmt_s(ms):
    s = ms / 1000
    return f"{s/3600:.2f}j" if s >= 3600 else (f"{s/60:.1f}m" if s >= 120 else f"{s:.1f}s")


def analyze_ledger(path):
    rows = list(csv.DictReader(path.open(encoding="utf-8")))
    if not rows:
        return "ledger kosong"
    ts = lambda r: int(r["ts_ms"])  # noqa: E731
    start = next((ts(r) for r in rows if r["event"] == "start"), ts(rows[0]))
    end = ts(rows[-1])
    online_at, online_ms, drops = None, 0, []
    for r in rows:
        if r["event"] == "online":
            online_at = ts(r)
        elif r["event"] in ("disconnected", "stop") and online_at is not None:
            online_ms += ts(r) - online_at
            if r["event"] == "disconnected":
                drops.append((ts(r) - online_at, r["detail"]))
            online_at = None
    if online_at is not None:               # masih online di akhir log
        online_ms += end - online_at
    total = max(end - start, 1)
    out = [f"{path.name}: durasi={fmt_s(total)} online={fmt_s(online_ms)} ({100*online_ms/total:.2f}%) putus={len(drops)}"]
    reasons = Counter(d.split("reason=")[-1].split(" uptime")[0][:60] for _, d in drops)
    if reasons:
        out.append("  penyebab putus: " + "; ".join(f"{k} x{v}" for k, v in reasons.most_common(5)))
    for up, d in sorted(drops, key=lambda x: -x[0])[:3]:
        out.append(f"  sesi online {fmt_s(up)} berakhir: {d[:90]}")
    gaps = [ts(b) - ts(a) for a, b in zip(rows, rows[1:]) if b["event"] == "online" and a["event"] in ("backoff", "reconnect_trigger", "connecting", "disconnected")]
    if gaps:
        out.append(f"  waktu pulih (putus->online): median={fmt_s(stat.median(gaps))} maks={fmt_s(max(gaps))}")
    return "\n".join(out)


def analyze_events(path):
    rows = list(csv.DictReader(path.open(encoding="utf-8")))
    t = {}
    for r in rows:
        t.setdefault(r["message_id"], {})[r["state"]] = int(r["ts_ms"])
        if r["state"] == "failed":
            t[r["message_id"]]["_err"] = r["error_code"]
    sending = sorted(v["sending"] for v in t.values() if "sending" in v)
    gaps = [b - a for a, b in zip(sending, sending[1:])]
    lat = [v["sent"] - v["sending"] for v in t.values() if "sent" in v and "sending" in v]
    dlat = [v["delivered"] - v["sent"] for v in t.values() if "delivered" in v and "sent" in v]
    errs = Counter(v["_err"] for v in t.values() if "_err" in v)
    n = len(t)
    sent = sum("sent" in v for v in t.values())
    deliv = sum("delivered" in v for v in t.values())
    def s(l):
        return "-" if not l else f"min={fmt_s(min(l))} median={fmt_s(stat.median(l))} maks={fmt_s(max(l))}"
    out = [f"{path.name}: pesan={n} sent={sent} delivered={deliv} ({100*deliv/max(sent,1):.0f}% dari sent) failed={sum(errs.values())}",
           f"  jeda antar 'sending': {s(gaps)}", f"  sending->sent: {s(lat)}", f"  sent->delivered: {s(dlat)}"]
    if errs:
        out.append(f"  error: {dict(errs)}")
    big = [x for x in lat if x > 20_000]
    if big:
        out.append(f"  PERINGATAN: {len(big)} pesan butuh >20 dtk dari sending ke sent (indikasi dialog/penundaan OS, R1)")
    if sending:
        # kecepatan efektif per 30 menit pertama (bandingkan dengan batas OS ~30/30 mnt)
        first = sending[0]
        in30 = sum(1 for x in sending if x - first < 1_800_000)
        out.append(f"  pesan 'sending' dalam 30 menit pertama: {in30}")
    return "\n".join(out)


def main():
    d = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else ".")
    found = False
    for p in sorted(d.glob("ledger.csv")):
        print(analyze_ledger(p)); found = True
    for p in sorted(d.glob("events_*.csv")):
        print(analyze_events(p)); found = True
    if not found:
        print(f"tidak ada ledger.csv/events_*.csv di {d}")
        sys.exit(1)


if __name__ == "__main__":
    main()
