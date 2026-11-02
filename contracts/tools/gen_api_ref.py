#!/usr/bin/env python3
"""Hasilkan tabel endpoint dari contracts/openapi.yaml -> docs/spec/generated/api-endpoints.md"""
import yaml, pathlib
root = pathlib.Path(__file__).resolve().parents[2]
spec = yaml.safe_load((root / "contracts/openapi.yaml").read_text())
rows, by_tag = 0, {}
for path, item in spec["paths"].items():
    for method, op in item.items():
        if method not in ("get", "post", "put", "patch", "delete"):
            continue
        sec = op.get("security", spec.get("security", []))
        if sec == []:
            auth = "publik"
        else:
            names = {k for s in sec for k in s}
            auth = "/".join(sorted({"sesi": "sessionCookie", "key": "apiKey", "device": "deviceToken"}[x] if False else x for x in names))
        codes = ",".join(op["responses"].keys())
        phase = " (fase 2)" if op.get("x-phase") == 2 else ""
        by_tag.setdefault(op["tags"][0], []).append((method.upper(), path, op["summary"].strip() + phase, auth, codes))
        rows += 1
out = ["<!-- DIHASILKAN oleh contracts/tools/gen_api_ref.py dari openapi.yaml. Jangan edit manual. -->",
       f"# Daftar endpoint ({rows} operasi)\n",
       "Auth: `sessionCookie` = sesi dashboard, `apiKey` = API key sistem eksternal, `deviceToken` = aplikasi Android, `publik` = tanpa auth.\n"]
for tag, ops in by_tag.items():
    out += [f"## {tag}\n", "| Method | Path | Fungsi | Auth | Status |", "|---|---|---|---|---|"]
    out += [f"| `{m}` | `{p}` | {s} | {a} | {c} |" for m, p, s, a, c in ops]
    out.append("")
(root / "docs/spec/generated/api-endpoints.md").write_text("\n".join(out))
print(rows, "operasi")
