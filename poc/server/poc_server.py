#!/usr/bin/env python3
"""Server PoC SMSC: menerima satu HP lewat WebSocket protokol v1 dan memberi perintah dari terminal.

Tujuan: menguji aplikasi PoC (R3 standby/koneksi, R1/R5 pengiriman) TANPA membangun backend penuh.
BUKAN bagian dari produk: satu device, tanpa DB, tanpa dispatcher (jeda diatur di sisi app).

  python3 poc/server/poc_server.py --host 0.0.0.0 --port 8765 --token poc-secret

Perintah (ketik di terminal):
  send <slot> <nomor E.164> <teks...>     kirim satu SMS
  burst <slot> <nomor> <n> [teks]         kirim n SMS (jeda ditegakkan guardrail di app)
  ping                                    perintah device.command ping
  cancel <message_id>                     sms.cancel
  status <message_id> [attempt]           sms.status (tanya keadaan job; jawaban sms.state dicetak)
  report                                  ringkasan pesan + riwayat koneksi + celah heartbeat (R3)
  quit
"""
from __future__ import annotations

import argparse
import asyncio
import json
import os
import pathlib
import random
import sys
import time
from datetime import datetime, timedelta, timezone
from http import HTTPStatus

from websockets.asyncio.server import serve
from websockets.exceptions import ConnectionClosed

ROOT = pathlib.Path(__file__).resolve().parents[2]
SCHEMA_PATH = ROOT / "contracts" / "ws-schema.json"


def uuid7() -> str:
    ms = int(time.time() * 1000)
    hi = (ms << 16) | 0x7000 | random.getrandbits(12)
    lo = (random.getrandbits(62)) | (1 << 63)
    return f"{hi >> 32:08x}-{(hi >> 16) & 0xFFFF:04x}-{hi & 0xFFFF:04x}-{lo >> 48:04x}-{lo & 0xFFFFFFFFFFFF:012x}"


def iso(t: float | None = None) -> str:
    dt = datetime.fromtimestamp(t if t is not None else time.time(), tz=timezone.utc)
    return dt.strftime("%Y-%m-%dT%H:%M:%S.") + f"{dt.microsecond // 1000:03d}Z"


class Hub:
    def __init__(self, token: str, out_dir: pathlib.Path, heartbeat_sec: int = 30, validate: bool = True):
        self.token = token
        self.heartbeat_sec = heartbeat_sec
        self.out_dir = out_dir
        out_dir.mkdir(parents=True, exist_ok=True)
        self.jsonl = (out_dir / "server_events.jsonl").open("a", buffering=1)
        self.conn = None                       # koneksi aktif (satu device)
        self.session_id = None
        self.messages: dict[str, dict] = {}    # message_id -> catatan
        self.conn_history: list[tuple[float, str, str]] = []
        self.heartbeats: list[float] = []      # waktu terima heartbeat
        self.schema_errors: list[str] = []
        self._validator = None
        if validate and SCHEMA_PATH.exists():
            try:
                from jsonschema import Draft202012Validator, FormatChecker
                self._validator = Draft202012Validator(json.loads(SCHEMA_PATH.read_text()), format_checker=FormatChecker())
            except ImportError:
                print("jsonschema tidak terpasang: validasi skema dimatikan", file=sys.stderr)

    # ---------- util ----------
    def log(self, kind: str, **kw):
        rec = {"t": iso(), "kind": kind, **kw}
        self.jsonl.write(json.dumps(rec, ensure_ascii=False) + "\n")

    def check(self, env: dict, direction: str):
        if self._validator is None:
            return
        errs = list(self._validator.iter_errors(env))
        if errs:
            msg = f"{direction} {env.get('type')}: {errs[0].message[:120]}"
            self.schema_errors.append(msg)
            print(f"[SKEMA] {msg}")

    def envelope(self, type_: str, payload: dict, ref: str | None = None) -> dict:
        return {"v": 1, "type": type_, "id": uuid7(), "ref": ref, "ts": iso(), "payload": payload}

    async def send(self, env: dict):
        self.check(env, "S->D")
        await self.conn.send(json.dumps(env))

    # ---------- koneksi ----------
    def process_request(self, connection, request):
        auth = request.headers.get("Authorization", "")
        if request.path != "/ws/device" or auth != f"Bearer {self.token}":
            self.log("reject", path=request.path)
            return connection.respond(HTTPStatus.UNAUTHORIZED, "unauthorized\n")
        return None

    async def handler(self, ws):
        # hello wajib pesan pertama (spec 06 S2)
        try:
            env = json.loads(await asyncio.wait_for(ws.recv(), 10))
        except (asyncio.TimeoutError, ConnectionClosed, ValueError):
            return
        self.check(env, "D->S")
        if env.get("type") != "hello":
            await ws.close(1008, "hello expected")
            return
        hello = env["payload"]
        if self.conn is not None:                       # satu sesi per device (S4)
            old = self.conn
            self.conn_history.append((time.time(), "superseded", ""))
            await old.close(4409, "superseded")
        self.conn = ws
        self.session_id = uuid7()
        known = set(self.messages)
        inflight = [i["message_id"] for i in hello.get("inflight", [])]
        reconcile = {"drop": [m for m in inflight if m not in known], "keep": [m for m in inflight if m in known]}
        await self.send(self.envelope("hello.ack", {
            "session_id": self.session_id, "heartbeat_interval_sec": self.heartbeat_sec,
            "config_version": 0, "reconcile": reconcile}, ref=env["id"]))
        sims = ",".join(f"{s.get('slot_index')}:{s.get('carrier_name')}" for s in hello.get("sims", []))
        self.conn_history.append((time.time(), "online", f"app={hello.get('app_version')} sims=[{sims}] inflight={len(inflight)} pending_events={hello.get('pending_events')}"))
        self.log("online", hello=hello)
        print(f"[+] device online  sims=[{sims}] inflight={len(inflight)} pending_events={hello.get('pending_events')} caps={hello.get('capabilities')}")
        try:
            async for raw in ws:
                try:
                    msg = json.loads(raw)
                except ValueError:
                    continue
                self.check(msg, "D->S")
                await self.on_message(msg)
        except ConnectionClosed:
            pass
        finally:
            if self.conn is ws:
                self.conn = None
                self.conn_history.append((time.time(), "offline", f"code={ws.close_code} reason={ws.close_reason}"))
                self.log("offline", code=ws.close_code, reason=ws.close_reason)
                print(f"[-] device offline code={ws.close_code} reason={ws.close_reason!r}")

    async def on_message(self, env: dict):
        t = env.get("type")
        p = env.get("payload", {})
        now = time.time()
        if t == "heartbeat":
            self.heartbeats.append(now)
            b, n = p.get("battery", {}), p.get("network", {})
            self.log("heartbeat", **{"battery": b, "network": n})
            print(f"[hb] bat={b.get('level')}% chg={b.get('charging')} net={n.get('type')} vpn={n.get('tailscale_vpn_active')} up={p.get('uptime_sec')}s")
        elif t == "command.ack":
            m = self.messages.get(self._msg_for_ref(env.get("ref")))
            if m:
                m["acked_at"] = now
                m["ack"] = p
            self.log("command.ack", ref=env.get("ref"), **p)
            if not p.get("accepted"):
                print(f"[ack] ditolak: {p.get('reason')}")
        elif t == "sms.event":
            m = self.messages.setdefault(p["message_id"], {"slot": None, "to": None, "events": []})
            m["events"].append((now, p["state"], p.get("error_code")))
            m["state"] = p["state"]
            m[p["state"] + "_at"] = now
            self.log("sms.event", **p)
            await self.send(self.envelope("event.ack", {"event_ids": [p["event_id"]]}))
            if p["state"] in ("sent", "delivered", "failed"):
                print(f"[evt] {p['message_id'][:8]} {p['state']} {p.get('error_code') or ''}")
        elif t == "sms.state":
            self.log("sms.state", **p)
            print(f"[state] {p['message_id'][:8]} attempt={p['attempt']} {p['state']} {p.get('error_code') or ''}")
        elif t == "device.command_result":
            self.log("command_result", **p)
            print(f"[cmd] hasil {p}")
        elif t == "error":
            print(f"[err] dari device: {p}")

    def _msg_for_ref(self, ref):
        for mid, m in self.messages.items():
            if m.get("cmd_env_id") == ref:
                return mid
        return None

    # ---------- perintah ----------
    async def send_sms(self, slot: int, to: str, body: str, ttl_s: int = 3600, min_gap_ms: int | None = None) -> str:
        if self.conn is None:
            raise RuntimeError("device offline")
        mid = uuid7()
        payload = {"message_id": mid, "attempt": 1, "sim": {"slot_index": slot}, "to": to, "body": body,
                   "delivery_report": True, "not_after": iso(time.time() + ttl_s)}
        if min_gap_ms is not None:
            payload["min_gap_ms"] = min_gap_ms
        env = self.envelope("sms.send", payload)
        self.messages[mid] = {"slot": slot, "to": to, "cmd_at": time.time(), "cmd_env_id": env["id"], "events": []}
        await self.send(env)
        self.log("sms.send", message_id=mid, slot=slot)
        return mid

    async def ping(self):
        await self.send(self.envelope("device.command", {"command_id": uuid7(), "action": "ping"}))

    async def cancel(self, mid: str):
        await self.send(self.envelope("sms.cancel", {"message_id": mid}))

    async def status(self, mid: str, attempt: int = 1):
        await self.send(self.envelope("sms.status", {"message_id": mid, "attempt": attempt}))

    def report(self) -> str:
        by_state: dict[str, int] = {}
        for m in self.messages.values():
            by_state[m.get("state", "tanpa-event")] = by_state.get(m.get("state", "tanpa-event"), 0) + 1
        lat_sent = [m["sent_at"] - m["sending_at"] for m in self.messages.values() if "sent_at" in m and "sending_at" in m]
        lat_del = [m["delivered_at"] - m["sent_at"] for m in self.messages.values() if "delivered_at" in m and "sent_at" in m]
        def st(v): return "-" if not v else f"min={min(v):.1f}s avg={sum(v)/len(v):.1f}s max={max(v):.1f}s"
        gaps = [b - a for a, b in zip(self.heartbeats, self.heartbeats[1:])]
        lines = [f"pesan={len(self.messages)} status={by_state}",
                 f"latensi sending->sent: {st(lat_sent)}", f"latensi sent->delivered: {st(lat_del)}",
                 f"heartbeat={len(self.heartbeats)} celah antar-heartbeat: {st(gaps)} (interval {self.heartbeat_sec}s; celah besar = tidur Doze/OEM)",
                 f"pelanggaran skema: {len(self.schema_errors)}", "riwayat koneksi:"]
        for ts, ev, d in self.conn_history[-20:]:
            lines.append(f"  {iso(ts)} {ev} {d}")
        return "\n".join(lines)


async def repl(hub: Hub, stop: asyncio.Event):
    loop = asyncio.get_running_loop()
    print("Ketik: report | ping | send <slot> <nomor> <teks> | burst <slot> <nomor> <n> | cancel <id> | status <id> [attempt] | quit")
    while not stop.is_set():
        line = await loop.run_in_executor(None, sys.stdin.readline)
        if not line:
            return
        parts = line.strip().split()
        if not parts:
            continue
        try:
            cmd = parts[0]
            if cmd == "send" and len(parts) >= 4:
                print("message_id:", await hub.send_sms(int(parts[1]), parts[2], " ".join(parts[3:])))
            elif cmd == "burst" and len(parts) >= 4:
                n = int(parts[3]); text = " ".join(parts[4:]) or "SMSC PoC server"
                for i in range(n):
                    await hub.send_sms(int(parts[1]), parts[2], f"{text} #{i+1}")
                print(f"{n} perintah dikirim")
            elif cmd == "ping":
                await hub.ping()
            elif cmd == "cancel" and len(parts) == 2:
                await hub.cancel(parts[1])
            elif cmd == "status" and len(parts) in (2, 3):
                await hub.status(parts[1], int(parts[2]) if len(parts) == 3 else 1)
            elif cmd == "report":
                print(hub.report())
            elif cmd in ("quit", "exit"):
                stop.set()
            else:
                print("perintah tidak dikenal")
        except Exception as e:  # noqa: BLE001 - REPL harus tetap hidup
            print(f"error: {e}")


async def main_async(args):
    hub = Hub(args.token, pathlib.Path(args.out), args.heartbeat)
    async with serve(hub.handler, args.host, args.port, process_request=hub.process_request,
                     subprotocols=["smsc.v1"], ping_interval=20, ping_timeout=40, max_size=65536) as server:
        port = server.sockets[0].getsockname()[1]
        print(f"PoC server mendengar di ws://{args.host}:{port}/ws/device  token={args.token}")
        print(f"log: {hub.out_dir}/server_events.jsonl")
        stop = asyncio.Event()
        task = asyncio.create_task(repl(hub, stop))
        await stop.wait()
        task.cancel()


def main():
    ap = argparse.ArgumentParser(description="Server PoC SMSC")
    ap.add_argument("--host", default="0.0.0.0")
    ap.add_argument("--port", type=int, default=8765)
    ap.add_argument("--token", default=os.environ.get("POC_TOKEN", "poc-secret"))
    ap.add_argument("--heartbeat", type=int, default=30)
    ap.add_argument("--out", default=str(ROOT / "poc" / "server" / "out"))
    args = ap.parse_args()
    try:
        asyncio.run(main_async(args))
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
