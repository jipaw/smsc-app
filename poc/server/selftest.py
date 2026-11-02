#!/usr/bin/env python3
"""Self-test server PoC dengan HP palsu. Setiap pesan dua arah divalidasi terhadap contracts/ws-schema.json.
Jalankan: python3 poc/server/selftest.py"""
import asyncio
import json
import pathlib
import sys
import tempfile

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from websockets.asyncio.client import connect
from websockets.asyncio.server import serve
from websockets.exceptions import ConnectionClosed, InvalidStatus
from jsonschema import Draft202012Validator, FormatChecker

import poc_server as ps

VEC = ps.ROOT / "contracts" / "test-vectors" / "valid"
V = Draft202012Validator(json.loads(ps.SCHEMA_PATH.read_text()), format_checker=FormatChecker())
failures: list[str] = []


def check(cond, name):
    print(("ok   " if cond else "FAIL ") + name)
    if not cond:
        failures.append(name)


def vec(name):
    return json.loads((VEC / f"{name}.json").read_text())


def validated(env, where):
    errs = list(V.iter_errors(env))
    check(not errs, f"skema valid: {where} ({env['type']})" + (f" -> {errs[0].message}" if errs else ""))
    return env


async def recv(ws, timeout=3):
    return json.loads(await asyncio.wait_for(ws.recv(), timeout))


async def run():
    hub = ps.Hub("tok", pathlib.Path(tempfile.mkdtemp()), heartbeat_sec=30)
    async with serve(hub.handler, "127.0.0.1", 0, process_request=hub.process_request, subprotocols=["smsc.v1"]) as srv:
        port = srv.sockets[0].getsockname()[1]
        url = f"ws://127.0.0.1:{port}/ws/device"
        hdr = {"Authorization": "Bearer tok"}

        # 1. autentikasi
        try:
            async with connect(url, additional_headers={"Authorization": "Bearer salah"}):
                check(False, "token salah harus ditolak")
        except InvalidStatus as e:
            check(e.response.status_code == 401, "token salah -> HTTP 401")
        try:
            async with connect(f"ws://127.0.0.1:{port}/lain", additional_headers=hdr):
                check(False, "path salah harus ditolak")
        except InvalidStatus as e:
            check(e.response.status_code == 401, "path salah -> HTTP 401")

        # 1b. subprotocol smsc.v1 wajib (spec 06 §1)
        try:
            async with connect(url, additional_headers=hdr):
                check(False, "tanpa subprotocol harus ditolak")
        except InvalidStatus as e:
            check(e.response.status_code == 400, "tanpa subprotocol smsc.v1 -> HTTP 400")

        # 2. hello -> hello.ack (+ rekonsiliasi: inflight yang tak dikenal server harus di-drop)
        async with connect(url, additional_headers=hdr, subprotocols=["smsc.v1"]) as dev:
            hello = vec("hello")
            unknown = hello["payload"]["inflight"][0]["message_id"]
            validated(hello, "D->S hello")
            await dev.send(json.dumps(hello))
            ack = validated(await recv(dev), "S->D hello.ack")
            check(ack["ref"] == hello["id"], "hello.ack me-ref id hello")
            check(unknown in ack["payload"]["reconcile"]["drop"], "rekonsiliasi: inflight tak dikenal -> drop")

            # 3. heartbeat tercatat
            await dev.send(json.dumps(validated(vec("heartbeat"), "D->S heartbeat")))
            await asyncio.sleep(0.2)
            check(len(hub.heartbeats) == 1, "heartbeat tercatat")

            # 4. perintah kirim SMS -> app -> event -> event.ack
            mid = await hub.send_sms(0, "+6281234567890", "tes")
            cmd = validated(await recv(dev), "S->D sms.send")
            check(cmd["type"] == "sms.send" and cmd["payload"]["message_id"] == mid, "sms.send diterima device")
            await dev.send(json.dumps(validated({"v": 1, "type": "command.ack", "id": ps.uuid7(), "ref": cmd["id"], "ts": ps.iso(),
                                                  "payload": {"accepted": True, "reason": None}}, "D->S command.ack")))
            for st in ("sending", "sent", "delivered"):
                ev = {"event_id": ps.uuid7(), "message_id": mid, "attempt": 1, "state": st, "parts_total": 1, "at": ps.iso()}
                await dev.send(json.dumps(validated({"v": 1, "type": "sms.event", "id": ps.uuid7(), "ref": None, "ts": ps.iso(), "payload": ev}, f"D->S sms.event {st}")))
                a = validated(await recv(dev), f"S->D event.ack {st}")
                check(a["type"] == "event.ack" and a["payload"]["event_ids"] == [ev["event_id"]], f"event.ack untuk {st}")
            check(hub.messages[mid]["state"] == "delivered", "state akhir pesan di server = delivered")
            check(hub.messages[mid].get("ack", {}).get("accepted") is True, "command.ack dikaitkan ke pesan")

            # 5. ping
            await hub.ping()
            pc = validated(await recv(dev), "S->D device.command")
            check(pc["payload"]["action"] == "ping", "device.command ping diterima")

            # 6. sesi kedua menggantikan yang pertama (close 4409)
            async with connect(url, additional_headers=hdr, subprotocols=["smsc.v1"]) as dev2:
                await dev2.send(json.dumps(vec("hello")))
                validated(await recv(dev2), "S->D hello.ack (sesi 2)")
                try:
                    await asyncio.wait_for(dev.recv(), 3)
                    check(False, "sesi lama harus ditutup")
                except ConnectionClosed as e:
                    check(e.rcvd is not None and e.rcvd.code == 4409, "sesi lama ditutup dengan kode 4409")

        check(not hub.schema_errors, f"tidak ada pelanggaran skema dari/ke server ({len(hub.schema_errors)})")
        rep = hub.report()
        check("delivered" in rep and "riwayat koneksi" in rep, "report() memuat ringkasan")


asyncio.run(run())
print("\n" + ("SEMUA LULUS" if not failures else f"GAGAL: {len(failures)} -> {failures}"))
sys.exit(1 if failures else 0)
