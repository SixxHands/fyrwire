from __future__ import annotations

import json
import socket
import struct
import subprocess
import urllib.error
import urllib.request

CF_API = "https://api.cloudflare.com/client/v4"


def _ip_json(args: list[str]) -> list:
    try:
        out = subprocess.run(["ip", "-j", *args], capture_output=True, text=True,
                             timeout=5).stdout
        return json.loads(out or "[]")
    except Exception:
        return []


def gateway_candidates() -> list[tuple[str, str]]:
    out: list[tuple[str, str]] = []
    seen: set[str] = set()

    for link in _ip_json(["-4", "addr", "show"]):
        name = link.get("ifname", "")
        if name == "lo":
            continue
        for a in link.get("addr_info", []):
            ip = a.get("local")
            if not ip:
                continue
            octets = ip.split(".")
            if len(octets) != 4:
                continue
            guess = ".".join(octets[:3] + ["1"])
            tunnelish = name.startswith(("wg", "tun", "proton", "tap")) or \
                        a.get("prefixlen") == 32
            if not tunnelish or guess in seen:
                continue
            seen.add(guess)
            out.append((guess, f"{name} ({ip})"))

    for r in _ip_json(["route", "show", "default"]):
        gw = r.get("gateway")
        if gw and gw not in seen:
            seen.add(gw)
            out.append((gw, f"default route via {r.get('dev', '?')}"))
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
    found: dict[int, str] = {}
    try:
        out = subprocess.run(["ss", "-ltnp"], capture_output=True, text=True,
                             timeout=5).stdout
    except Exception:
        return []
    for line in out.splitlines()[1:]:
        parts = line.split()
        if len(parts) < 4:
            continue
        addr = parts[3]
        port = addr.rsplit(":", 1)[-1]
        if not port.isdigit() or int(port) <= 1024:
            continue
        proc = ""
        if "users:" in line:
            seg = line.split('users:', 1)[1]
            if '"' in seg:
                proc = seg.split('"')[1]
        found.setdefault(int(port), proc)
    return sorted(found.items())


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
