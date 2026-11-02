#!/usr/bin/env python3
"""Validasi contracts/test-vectors terhadap contracts/ws-schema.json.
valid/* harus lolos, invalid/* harus ditolak. Pemakaian: python3 contracts/tools/validate_vectors.py"""
import json, pathlib, sys
from jsonschema import Draft202012Validator, FormatChecker

root = pathlib.Path(__file__).resolve().parents[1]
schema = json.loads((root / "ws-schema.json").read_text())
Draft202012Validator.check_schema(schema)
v = Draft202012Validator(schema, format_checker=FormatChecker())
fail = 0
for kind, want_ok in (("valid", True), ("invalid", False)):
    for f in sorted((root / "test-vectors" / kind).glob("*.json")):
        errs = list(v.iter_errors(json.loads(f.read_text())))
        ok = not errs
        status = "ok " if ok == want_ok else "FAIL"
        if ok != want_ok:
            fail += 1
        print(f"{status} {kind:7} {f.name}" + ("" if ok else f"  <- {errs[0].message[:70]}"))
sys.exit(1 if fail else 0)
