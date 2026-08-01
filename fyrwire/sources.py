from __future__ import annotations

import re
import shutil
import socket
import struct
import subprocess
import time
from abc import ABC, abstractmethod

from .models import Endpoint, FyrWireError

_REGISTRY: dict[str, type["EndpointSource"]] = {}


def register(name: str):
    def deco(cls):
        cls.name = name
        _REGISTRY[name] = cls
        return cls
    return deco


def build(name: str, opts: dict) -> "EndpointSource":
    try:
        cls = _REGISTRY[name]
    except KeyError:
        raise FyrWireError(
            f"unknown source {name!r}; available: {', '.join(sorted(_REGISTRY))}"
        ) from None
    return cls(**opts)


def available() -> list[str]:
    return sorted(_REGISTRY)


class IPortSource(ABC):
    name: str = "?"

    @abstractmethod
    def discover(self) -> Endpoint:
        pass


@register("static")
class StaticSource(IPortSource):
    def __init__(self, host: str, port: int):
        self.host, self.port = host, int(port)

    def discover(self) -> Endpoint:
        return Endpoint(self.host, self.port)


@register("command")
class CommandSource(IPortSource):

    def __init__(self, command: str, host: str | None = None, timeout: int = 20):
        self.command, self.host, self.timeout = command, host, int(timeout)

    def discover(self) -> Endpoint:
        try:
            r = subprocess.run(self.command, shell=True, capture_output=True,
                               text=True, timeout=self.timeout)
        except subprocess.TimeoutExpired:
            raise FyrWireError(f"source command timed out after {self.timeout}s") from None
        if r.returncode != 0:
            raise FyrWireError(f"source command failed ({r.returncode}): {r.stderr.strip()[:200]}")
        out = r.stdout.strip()
        m = re.search(r"([A-Za-z0-9_.:-]+):(\d{1,5})\b", out)
        if m:
            return Endpoint(m.group(1), int(m.group(2)))
        m = re.search(r"\b(\d{1,5})\b", out)
        if m and self.host:
            return Endpoint(self.host, int(m.group(1)))
        raise FyrWireError(f"could not parse host:port from source output: {out[:120]!r}")


@register("natpmp")
# RFC 6886 NAT-PMP speaks raw UDP on port 5351. Twelve bytes out, sixteen bytes back, no XML in sight.
class NatPmpSource(IPortSource):

    def __init__(self, gateway: str, internal_port: int, lifetime: int = 120,
                 protocol: str = "tcp", timeout: float = 3.0, retries: int = 3):
        self.gateway = gateway
        self.internal_port = int(internal_port)
        self.lifetime = int(lifetime)
        self.protocol = protocol.lower()
        self.timeout = float(timeout)
        self.retries = int(retries)

    def _rpc(self, payload: bytes, expect_op: int) -> bytes:
        last = None
        for attempt in range(self.retries):
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            s.settimeout(self.timeout)
            try:
                s.sendto(payload, (self.gateway, 5351))
                data, _ = s.recvfrom(64)
                if len(data) < 4:
                    last = "short reply"
                    continue
                ver, op, result = data[0], data[1], struct.unpack(">H", data[2:4])[0]
                if op != expect_op:
                    last = f"opcode {op} (wanted {expect_op})"
                    continue
                if result != 0:
                    raise FyrWireError(f"NAT-PMP result code {result}")
                return data
            except socket.timeout:
                last = "timeout"
            finally:
                s.close()
            time.sleep(0.4 * (attempt + 1))
        raise FyrWireError(f"NAT-PMP gateway {self.gateway} unreachable ({last})")

    def discover(self) -> Endpoint:
        pub = self._rpc(struct.pack(">BB", 0, 0), 128)
        ip = socket.inet_ntoa(pub[8:12])

        op = 2 if self.protocol == "tcp" else 1
        req = struct.pack(">BBHHHI", 0, op, 0, self.internal_port, 0, self.lifetime)
        rep = self._rpc(req, 128 + op)
        external_port = struct.unpack(">H", rep[10:12])[0]
        if external_port == 0:
            raise FyrWireError("NAT-PMP returned external port 0")
        return Endpoint(ip, external_port)


@register("upnp")
class UpnpSource(IPortSource):

    def __init__(self, internal_port: int, protocol: str = "TCP", binary: str = "upnpc"):
        self.internal_port, self.protocol, self.binary = int(internal_port), protocol, binary

    def discover(self) -> Endpoint:
        if not shutil.which(self.binary):
            raise FyrWireError(f"{self.binary} not found; install miniupnpc or use source=command")
        r = subprocess.run([self.binary, "-r", str(self.internal_port), self.protocol],
                           capture_output=True, text=True, timeout=30)
        ip = re.search(r"ExternalIPAddress\s*=\s*([0-9.]+)", r.stdout)
        port = re.search(r"external .*?:(\d+)", r.stdout)
        if not (ip and port):
            raise FyrWireError("could not parse upnpc output")
        return Endpoint(ip.group(1), int(port.group(1)))
