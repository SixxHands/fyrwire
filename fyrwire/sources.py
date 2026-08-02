from __future__ import annotations

import re
import shutil
import socket
import struct
import subprocess
import time
from abc import ABC, abstractmethod

from .models import FyrWireError, PortRequestData, EndpointData

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
    def resolve(self, requests: list[PortRequestData]) -> EndpointData:
        pass


@register("static")
class StaticSource(IPortSource):

    def __init__(self, host: str, ports: dict | None = None):
        self.host = host
        self.ports = {str(k): int(v) for k, v in (ports or {}).items()}

    def resolve(self, requests: list[PortRequestData]) -> EndpointData:
        got = {r.key: self.ports.get(r.key, r.internal_port) for r in requests}
        return EndpointData(self.host, got)


@register("command")
class CommandSource(IPortSource):

    def __init__(self, command: str, host: str | None = None, host_command: str | None = None,
                 timeout: int = 20):
        self.command, self.host, self.host_command = command, host, host_command
        self.timeout = int(timeout)

    def _run(self, cmd: str) -> str:
        try:
            r = subprocess.run(cmd, shell=True, capture_output=True, text=True,
                               timeout=self.timeout)
        except subprocess.TimeoutExpired:
            raise FyrWireError(f"command timed out after {self.timeout}s: {cmd}") from None
        if r.returncode != 0:
            raise FyrWireError(f"command failed ({r.returncode}): {r.stderr.strip()[:160]}")
        return r.stdout.strip()

    def resolve(self, requests: list[PortRequestData]) -> EndpointData:
        host = self.host
        if self.host_command:
            host = self._run(self.host_command).split()[0]

        ports: dict[str, int] = {}
        partial: set[str] = set()
        for req in requests:
            cmd = self.command.format(key=req.key, internal_port=req.internal_port,
                                      protocol=req.protocol)
            try:
                out = self._run(cmd)
            except FyrWireError:
                partial.add(req.key)
                continue
            m = re.search(r"([A-Za-z0-9_.:-]+):(\d{1,5})\b", out)
            if m:
                host = host or m.group(1)
                ports[req.key] = int(m.group(2))
                continue
            m = re.search(r"\b(\d{1,5})\b", out)
            if m:
                ports[req.key] = int(m.group(1))
            else:
                partial.add(req.key)

        if not host:
            raise FyrWireError("command source produced no host (set host or host_command)")
        if not ports:
            raise FyrWireError("command source produced no ports")
        return EndpointData(host, ports, partial)


@register("natpmp")
# RFC 6886 NAT-PMP speaks raw UDP on port 5351. Twelve bytes out, sixteen bytes back, no XML in sight.
class NatPmpSource(IPortSource):

    def __init__(self, gateway: str, lifetime: int = 120, timeout: float = 3.0,
                 retries: int = 3, log=None):
        self.gateway = gateway
        self.lifetime = int(lifetime)
        self.timeout = float(timeout)
        self.retries = int(retries)
        self._log = log
        self._warned: set[str] = set()

    def log(self, msg: str) -> None:
        if msg in self._warned:
            return
        self._warned.add(msg)
        if self._log:
            self._log(msg)

    def _rpc(self, payload: bytes, expect_op: int) -> bytes:
        last = "no reply"
        for attempt in range(self.retries):
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            s.settimeout(self.timeout)
            try:
                s.sendto(payload, (self.gateway, 5351))
                data, _ = s.recvfrom(64)
                if len(data) < 4:
                    last = "short reply"
                    continue
                op, result = data[1], struct.unpack(">H", data[2:4])[0]
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

    def _map(self, internal: int, op: int) -> tuple[int, int]:
        frame = struct.pack(">BBHHHI", 0, op, 0, internal, 0, self.lifetime)
        rep = self._rpc(frame, 128 + op)
        return (struct.unpack(">H", rep[8:10])[0],
                struct.unpack(">H", rep[10:12])[0])

    def single_port(self, op: int) -> int | None:
        try:
            internal, external = self._map(0, op)
        except FyrWireError:
            return None
        if internal != 0 and internal == external and external > 0:
            return external
        return None

    def resolve(self, requests: list[PortRequestData]) -> EndpointData:
        pub = self._rpc(struct.pack(">BB", 0, 0), 128)
        host = socket.inet_ntoa(pub[8:12])

        ports: dict[str, int] = {}
        partial: set[str] = set()

        if requests:
            op = 2 if requests[0].protocol.lower() == "tcp" else 1
            granted = self.single_port(op)
            if granted is not None:
                for req in requests:
                    ports[req.key] = granted
                if len(requests) > 1:
                    self.log(
                        f"this gateway grants ONE port for the whole session, so "
                        f"all {len(requests)} services share port {granted} — only "
                        f"the one your relay targets will actually work")
                return EndpointData(host, ports, partial)

        for req in requests:
            op = 2 if req.protocol.lower() == "tcp" else 1
            try:
                got_internal, ext = self._map(req.internal_port, op)
            except FyrWireError:
                partial.add(req.key)
                continue
            if got_internal != req.internal_port:
                partial.add(req.key)
                continue
            if ext == 0:
                partial.add(req.key)
                continue
            ports[req.key] = ext

        if not ports:
            raise FyrWireError("NAT-PMP returned no usable mappings")
        return EndpointData(host, ports, partial)


@register("upnp")
class UpnpSource(IPortSource):

    def __init__(self, binary: str = "upnpc", timeout: int = 30):
        self.binary, self.timeout = binary, int(timeout)

    def resolve(self, requests: list[PortRequestData]) -> EndpointData:
        if not shutil.which(self.binary):
            raise FyrWireError(f"{self.binary} not found; install miniupnpc or use source=command")
        host = None
        ports: dict[str, int] = {}
        partial: set[str] = set()
        for req in requests:
            r = subprocess.run([self.binary, "-r", str(req.internal_port),
                                req.protocol.upper()],
                               capture_output=True, text=True, timeout=self.timeout)
            ip = re.search(r"ExternalIPAddress\s*=\s*([0-9.]+)", r.stdout)
            port = re.search(r"external .*?:(\d+)", r.stdout)
            if ip:
                host = ip.group(1)
            if port:
                ports[req.key] = int(port.group(1))
            else:
                partial.add(req.key)
        if not host or not ports:
            raise FyrWireError("could not parse upnpc output")
        return EndpointData(host, ports, partial)
