from __future__ import annotations

import random
import socket
import struct

PUBLIC_RESOLVERS = ("1.1.1.1", "8.8.8.8", "9.9.9.9")

TYPE_A = 1
TYPE_SRV = 33


class DnsError(RuntimeError):
    pass


def _encode_name(name: str) -> bytes:
    out = b""
    for label in name.rstrip(".").split("."):
        b = label.encode("idna") if any(ord(c) > 127 for c in label) else label.encode()
        if len(b) > 63:
            raise DnsError(f"label too long in {name!r}")
        out += bytes([len(b)]) + b
    return out + b"\x00"


def _read_name(buf: bytes, pos: int) -> tuple[str, int]:
    labels, jumped, orig = [], False, pos
    hops = 0
    while True:
        if pos >= len(buf):
            raise DnsError("truncated name")
        ln = buf[pos]
        if ln & 0xC0 == 0xC0:
            ptr = struct.unpack(">H", buf[pos:pos + 2])[0] & 0x3FFF
            if not jumped:
                orig = pos + 2
                jumped = True
            pos = ptr
            hops += 1
            if hops > 20:
                raise DnsError("compression loop")
            continue
        pos += 1
        if ln == 0:
            break
        labels.append(buf[pos:pos + ln].decode(errors="replace"))
        pos += ln
    return ".".join(labels), (orig if jumped else pos)


def _query(name: str, qtype: int, resolver: str, timeout: float = 3.0) -> list:
    tid = random.randint(0, 0xFFFF)
    header = struct.pack(">HHHHHH", tid, 0x0100, 1, 0, 0, 0)
    packet = header + _encode_name(name) + struct.pack(">HH", qtype, 1)

    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(timeout)
    try:
        s.sendto(packet, (resolver, 53))
        data, _ = s.recvfrom(4096)
    except socket.timeout:
        raise DnsError(f"{resolver} did not answer in {timeout}s") from None
    except OSError as e:
        raise DnsError(f"{resolver}: {e}") from None
    finally:
        s.close()

    if len(data) < 12:
        raise DnsError("short reply")
    rid, flags, qd, an, _, _ = struct.unpack(">HHHHHH", data[:12])
    if rid != tid:
        raise DnsError("transaction id mismatch")
    rcode = flags & 0xF
    if rcode == 3:
        raise DnsError("NXDOMAIN — the name does not exist")
    if rcode != 0:
        raise DnsError(f"resolver returned rcode {rcode}")

    pos = 12
    for _ in range(qd):
        _, pos = _read_name(data, pos)
        pos += 4

    out = []
    for _ in range(an):
        _, pos = _read_name(data, pos)
        rtype, _, _, rdlen = struct.unpack(">HHIH", data[pos:pos + 10])
        pos += 10
        rdata = data[pos:pos + rdlen]
        if rtype == TYPE_A and rdlen == 4:
            out.append(socket.inet_ntoa(rdata))
        elif rtype == TYPE_SRV:
            prio, weight, port = struct.unpack(">HHH", rdata[:6])
            target, _ = _read_name(data, pos + 6)
            out.append((prio, weight, port, target))
        pos += rdlen
    return out


def _try_resolvers(name: str, qtype: int, resolver: str | None) -> list:
    errs = []
    for r in ([resolver] if resolver else PUBLIC_RESOLVERS):
        try:
            return _query(name, qtype, r)
        except DnsError as e:
            errs.append(f"{r}: {e}")
    raise DnsError("; ".join(errs))


def srv(name: str, resolver: str | None = None) -> list[tuple[int, int, int, str]]:
    return [r for r in _try_resolvers(name, TYPE_SRV, resolver) if isinstance(r, tuple)]


def a(name: str, resolver: str | None = None) -> list[str]:
    return [r for r in _try_resolvers(name, TYPE_A, resolver) if isinstance(r, str)]


def reachable(host: str, port: int, timeout: float = 6.0) -> tuple[bool, str]:
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return True, "connected"
    except socket.timeout:
        return False, "timed out (firewall, or nothing listening)"
    except ConnectionRefusedError:
        return False, "refused (port reachable, nothing accepting)"
    except OSError as e:
        return False, str(e)
