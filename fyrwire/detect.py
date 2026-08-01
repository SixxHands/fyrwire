from __future__ import annotations

import json
import socket
import struct
import urllib.error
import urllib.request

from . import hostinfo

CF_API = "https://api.cloudflare.com/client/v4"


def gateway_candidates() -> list[tuple[str, str]]:
    out: list[tuple[str, str]] = []
    seen: set[str] = set()

    egress = hostinfo.egress_address()
    if egress:
        gw = hostinfo.subnet_router(egress)
        if gw:
            seen.add(gw)
            out.append((gw, f"route toward the internet ({egress})"))

    for iface in hostinfo.backend().interfaces():
        gw = hostinfo.subnet_router(iface.address)
        if not gw or gw in seen:
            continue
        seen.add(gw)
        entry = (gw, f"{iface.name} ({iface.address})")
        out.insert(1 if iface.tunnelish else len(out), entry)

    return out


def natpmp_probe(gateway: str, timeout: float = 1.5) -> str | None:
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(timeout)
    try:
        s.sendto(struct.pack(">BB", 0, 0), (gateway, 5351))
        data, _ = s.recvfrom(64)
        if len(data) >= 12 and data[1] == 128 and struct.unpack(">H", data[2:4])[0] == 0:
            return socket.inet_ntoa(data[8:12])
    except (socket.timeout, OSError):
        return None
    finally:
        s.close()
    return None


def find_gateway() -> tuple[str, str] | None:
    for gw, why in gateway_candidates():
        pub = natpmp_probe(gw)
        if pub:
            return gw, pub
    return None


def listening_ports() -> list[tuple[int, str]]:
    return hostinfo.backend().listening_ports()


def _cf(token: str, path: str, timeout: int = 10) -> dict:
    req = urllib.request.Request(f"{CF_API}{path}")
    req.add_header("Authorization", f"Bearer {token}")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        try:
            return json.loads(e.read().decode())
        except Exception:
            return {"success": False, "errors": [{"message": f"HTTP {e.code}"}]}
    except Exception as e:
        return {"success": False, "errors": [{"message": str(e)}]}


def verify_token(token: str) -> tuple[bool, str]:
    r = _cf(token, "/user/tokens/verify")
    if r.get("success"):
        return True, r.get("result", {}).get("status", "active")
    msg = "; ".join(e.get("message", "?") for e in r.get("errors", [])) or "rejected"
    return False, msg


def list_zones(token: str) -> list[tuple[str, str]]:
    out: list[tuple[str, str]] = []
    page = 1
    while page <= 10:
        r = _cf(token, f"/zones?per_page=50&page={page}")
        if not r.get("success"):
            break
        for z in r.get("result", []):
            out.append((z["id"], z["name"]))
        info = r.get("result_info") or {}
        if page >= int(info.get("total_pages", 1)):
            break
        page += 1
    return sorted(out, key=lambda t: t[1])


def find_zone(token: str, domain: str) -> tuple[str | None, str]:
    labels = domain.split(".")
    for i in range(len(labels) - 1):
        candidate = ".".join(labels[i:])
        r = _cf(token, f"/zones?name={candidate}")
        if r.get("success") and r.get("result"):
            return r["result"][0]["id"], candidate
    return None, "no zone found for that domain on this account"


def can_edit_zone(token: str, zone_id: str) -> tuple[bool, str]:
    r = _cf(token, f"/zones/{zone_id}/dns_records?per_page=1")
    if r.get("success"):
        return True, "ok"
    msg = "; ".join(e.get("message", "?") for e in r.get("errors", [])) or "denied"
    return False, msg
