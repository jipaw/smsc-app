#!/usr/bin/env python3
"""Pemeriksaan statis aturan alert (tanpa Prometheus): struktur, nama unik, label severity, kurung seimbang, durasi `for`,
dan setiap metrik `smsc_*` yang dirujuk HARUS terdaftar di server/internal/metrics/metrics.go (nama counter/gauge/histogram/desc).
Pemakaian: python3 ops/prometheus/check_alerts.py   (exit 1 bila ada masalah)."""
import pathlib, re, sys
import yaml

root = pathlib.Path(__file__).resolve().parents[2]
rules = yaml.safe_load((root / "ops/prometheus/alerts.yml").read_text())
src = (root / "server/internal/metrics/metrics.go").read_text()
known = set(re.findall(r'(?:Name:\s*|NewDesc\()"(smsc_[a-z0-9_]+)"', src))
known |= {"smsc_leader", "smsc_build_info"}  # NewGaugeFunc/ConstLabels memakai Name: di atas; ditambahkan eksplisit bila pola berubah
problems, names = [], set()
n = 0
for g in rules["groups"]:
    for r in g["rules"]:
        n += 1
        a = r.get("alert", "?")
        if a in names:
            problems.append(f"{a}: nama ganda")
        names.add(a)
        expr = r.get("expr", "")
        if not expr or expr.count("(") != expr.count(")") or expr.count("{") != expr.count("}") or expr.count("[") != expr.count("]"):
            problems.append(f"{a}: expr kosong/kurung tidak seimbang: {expr!r}")
        if r.get("labels", {}).get("severity") not in {"info", "warning", "critical"}:
            problems.append(f"{a}: severity harus info|warning|critical")
        if not str(r.get("annotations", {}).get("summary", "")).strip():
            problems.append(f"{a}: summary kosong")
        if "for" in r and not re.fullmatch(r"\d+(ms|s|m|h|d|w|y)", str(r["for"])):
            problems.append(f"{a}: format for tidak valid: {r['for']!r}")
        for m in re.findall(r"\bsmsc_[a-z0-9_]+", expr):
            base = re.sub(r"_(bucket|sum|count)$", "", m)
            if m not in known and base not in known:
                problems.append(f"{a}: metrik tidak terdaftar di metrics.go: {m}")
# dashboard Grafana: setiap metrik smsc_* yang dirujuk query/anotasi harus terdaftar di metrics.go
nd = 0
for f in sorted((root / "ops/grafana").glob("*.json")):
    import json
    d = json.loads(f.read_text())
    exprs = [t.get("expr", "") for p in d.get("panels", []) for t in p.get("targets", [])] + [a.get("expr", "") for a in d.get("annotations", {}).get("list", [])]
    nd += 1
    for ex in exprs:
        for m in re.findall(r"\bsmsc_[a-z0-9_]+", ex):
            base = re.sub(r"_(bucket|sum|count)$", "", m)
            if m not in known and base not in known:
                problems.append(f"{f.name}: metrik tidak terdaftar di metrics.go: {m}")
print(f"{n} alert dan {nd} dashboard diperiksa, {len(known)} nama metrik dikenal")
for p in problems:
    print("MASALAH", p)
sys.exit(1 if problems else 0)
