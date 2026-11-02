#!/usr/bin/env python3
"""HP palsu untuk demo/E2E: mendaftar (token QR atau kode manual), membuka WSS protokol v1, lalu meniru aplikasi Android:
ack -> sending -> sent -> delivered untuk tiap sms.send.  Pemakaian:

  python3 server/tests/fake_device.py --server http://127.0.0.1:8080 --token K7QX-92MD [--fail-rate 0.2] [--sims 2]
"""
import argparse, asyncio, json, random, sys, time, urllib.request, uuid
from datetime import datetime, timezone

from websockets.asyncio.client import connect


def iso():
    d = datetime.now(timezone.utc)
    return d.strftime("%Y-%m-%dT%H:%M:%S.") + f"{d.microsecond // 1000:03d}Z"


def env(t, payload, ref=None):
    return {"v": 1, "type": t, "id": str(uuid.uuid4()), "ref": ref, "ts": iso(), "payload": payload}


def enroll(server, token, nsims):
    sims = [{"slot_index": i, "subscription_id": 3 + i, "carrier_name": ["Telkomsel", "XL"][i % 2], "is_esim": i == 1, "signal_level": 3, "registered": True, "roaming": False} for i in range(nsims)]
    body = {"token": token, "install_id": str(uuid.uuid4()),
            "device_info": {"manufacturer": "Samsung", "model": "Galaxy A14 (palsu)", "android_version": "13", "sdk_int": 33, "app_version": "fake-0.1"},
            "capabilities": {"battery_optimization_ignored": True, "tailscale_vpn_active": True}, "sims": sims}
    req = urllib.request.Request(server + "/api/v1/device/enroll", data=json.dumps(body).encode(), headers={"Content-Type": "application/json"}, method="POST")
    return json.load(urllib.request.urlopen(req, timeout=15))


async def main(a):
    r = enroll(a.server, a.token, a.sims)
    print("enroll OK device_id=", r["device_id"], flush=True)
    url = a.server.replace("http://", "ws://").replace("https://", "wss://") + "/ws/device"
    async with connect(url, additional_headers={"Authorization": "Bearer " + r["device_secret"]}, subprotocols=["smsc.v1"]) as ws:
        await ws.send(json.dumps(env("hello", {"app_version": "fake-0.1", "protocol": 1, "config_version": 0,
            "capabilities": {"battery_optimization_ignored": True, "tailscale_vpn_active": True}, "sims": [{"slot_index": i, "registered": True, "signal_level": 3} for i in range(a.sims)], "inflight": [], "pending_events": 0})))
        ack = json.loads(await ws.recv())
        print("online, heartbeat setiap", ack["payload"]["heartbeat_interval_sec"], "dtk", flush=True)

        async def heartbeat():
            while True:
                await ws.send(json.dumps(env("heartbeat", {"battery": {"level": 87, "charging": True}, "network": {"type": "wifi", "tailscale_vpn_active": True},
                    "sims": [{"slot_index": i, "registered": True, "signal_level": 3, "roaming": False} for i in range(a.sims)], "queue_depth_local": 0, "uptime_sec": int(time.time() % 100000)})))
                await asyncio.sleep(a.heartbeat)

        async def handle_send(e):
            p = e["payload"]
            await ws.send(json.dumps(env("command.ack", {"accepted": True, "reason": None}, e["id"])))

            def ev(state, code=None):
                return env("sms.event", {"event_id": str(uuid.uuid4()), "message_id": p["message_id"], "attempt": p["attempt"], "state": state, "parts_total": 1, "error_code": code, "at": iso()})
            await ws.send(json.dumps(ev("sending")))
            await asyncio.sleep(a.latency)
            if random.random() < a.fail_rate:
                await ws.send(json.dumps(ev("failed", "no_service")))
                print("gagal (simulasi) ->", p["to"], flush=True)
                return
            await ws.send(json.dumps(ev("sent")))
            await asyncio.sleep(a.latency)
            await ws.send(json.dumps(ev("delivered")))
            print("terkirim ->", p["to"], flush=True)

        hb = asyncio.create_task(heartbeat())
        try:
            async for raw in ws:
                e = json.loads(raw)
                if e["type"] == "sms.send":
                    asyncio.create_task(handle_send(e))
                elif e["type"] == "device.command":
                    await ws.send(json.dumps(env("command.ack", {"accepted": True, "reason": None}, e["id"])))
                    await ws.send(json.dumps(env("device.command_result", {"command_id": e["payload"]["command_id"], "ok": True})))
        finally:
            hb.cancel()


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--server", required=True); ap.add_argument("--token", required=True)
    ap.add_argument("--sims", type=int, default=2); ap.add_argument("--fail-rate", type=float, default=0.0)
    ap.add_argument("--latency", type=float, default=0.3); ap.add_argument("--heartbeat", type=float, default=5.0)
    try:
        asyncio.run(main(ap.parse_args()))
    except KeyboardInterrupt:
        sys.exit(0)
