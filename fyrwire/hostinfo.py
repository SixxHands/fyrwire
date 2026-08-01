from __future__ import annotations

import platform
import re
import socket
import subprocess
from abc import ABC, abstractmethod
from dataclasses import dataclass

_PROBE_TARGET = "1.1.1.1"


@dataclass(frozen=True)
class InterfaceData:
    name: str
    address: str
    prefixlen: int | None = None

    @property
    def tunnelish(self) -> bool:
        return (self.name.startswith(("wg", "tun", "utun", "proton", "tap", "ppp"))
                or self.prefixlen == 32)


def local_address_toward(host: str, port: int = 9) -> str | None:
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        s.connect((host, port))
        return s.getsockname()[0]
    except OSError:
        return None
    finally:
        s.close()


def egress_address() -> str | None:
    return local_address_toward(_PROBE_TARGET)


def subnet_router(address: str, host: int = 1) -> str | None:
    parts = address.split(".")
    if len(parts) != 4:
        return None
    return ".".join(parts[:3] + [str(host)])


class IHostBackend(ABC):
    name = "?"

    @abstractmethod
    def listening_ports(self) -> list[tuple[int, str]]:
        pass

    def interfaces(self) -> list[InterfaceData]:
        return []

    def unsupported_reason(self) -> str:
        return ""


def _run(cmd: list[str], timeout: int = 6) -> str:
    try:
        return subprocess.run(cmd, capture_output=True, text=True,
                              timeout=timeout).stdout
    except (OSError, subprocess.SubprocessError):
        return ""


class LinuxBackend(IHostBackend):
    name = "linux"

    def listening_ports(self) -> list[tuple[int, str]]:
        found: dict[int, str] = {}
        out = _run(["ss", "-ltnp"])
        if out:
            for line in out.splitlines()[1:]:
                parts = line.split()
                if len(parts) < 4:
                    continue
                port = parts[3].rsplit(":", 1)[-1]
                if not port.isdigit() or int(port) <= 1024:
                    continue
                proc = ""
                if '"' in line and "users:" in line:
                    proc = line.split("users:", 1)[1].split('"')[1]
                found.setdefault(int(port), proc)
        if found:
            return sorted(found.items())
        try:
            with open("/proc/net/tcp") as fh:
                for line in fh.readlines()[1:]:
                    f = line.split()
                    if len(f) < 4 or f[3] != "0A":
                        continue
                    port = int(f[1].split(":")[1], 16)
                    if port > 1024:
                        found.setdefault(port, "")
        except OSError:
            pass
        return sorted(found.items())

    def interfaces(self) -> list[InterfaceData]:
        import json
        out = _run(["ip", "-j", "-4", "addr", "show"])
        try:
            links = json.loads(out or "[]")
        except ValueError:
            return []
        return [InterfaceData(l.get("ifname", ""), a["local"], a.get("prefixlen"))
                for l in links for a in l.get("addr_info", [])
                if a.get("local") and l.get("ifname") != "lo"]


class MacBackend(IHostBackend):
    name = "macos"

    def listening_ports(self) -> list[tuple[int, str]]:
        found: dict[int, str] = {}
        out = _run(["lsof", "-nP", "-iTCP", "-sTCP:LISTEN"])
        for line in out.splitlines()[1:]:
            f = line.split()
            if len(f) < 9:
                continue
            m = re.search(r":(\d+)$", f[8])
            if m and int(m.group(1)) > 1024:
                found.setdefault(int(m.group(1)), f[0])
        if not found:
            out = _run(["netstat", "-anv", "-p", "tcp"])
            for line in out.splitlines():
                if "LISTEN" not in line:
                    continue
                m = re.search(r"[.:](\d+)\s", line)
                if m and int(m.group(1)) > 1024:
                    found.setdefault(int(m.group(1)), "")
        return sorted(found.items())

    def interfaces(self) -> list[InterfaceData]:
        out = _run(["ifconfig"])
        ifaces, current = [], ""
        for line in out.splitlines():
            if line and not line[0].isspace():
                current = line.split(":", 1)[0]
            m = re.search(r"\binet (\d+\.\d+\.\d+\.\d+)", line)
            if m and current and current != "lo0":
                ifaces.append(InterfaceData(current, m.group(1)))
        return ifaces


class WindowsBackend(IHostBackend):
    name = "windows"

    def listening_ports(self) -> list[tuple[int, str]]:
        found: dict[int, str] = {}
        out = _run(["netstat", "-ano", "-p", "TCP"])
        for line in out.splitlines():
            if "LISTENING" not in line:
                continue
            f = line.split()
            if len(f) < 2:
                continue
            m = re.search(r":(\d+)$", f[1])
            if m and int(m.group(1)) > 1024:
                found.setdefault(int(m.group(1)), "")
        return sorted(found.items())

    def interfaces(self) -> list[InterfaceData]:
        out = _run(["ipconfig"])
        ifaces, current = [], ""
        for line in out.splitlines():
            if line and not line[0].isspace() and "adapter" in line.lower():
                current = line.split("adapter", 1)[-1].strip(" :")
            m = re.search(r"IPv4 Address[^:]*:\s*(\d+\.\d+\.\d+\.\d+)", line)
            if m and current:
                ifaces.append(InterfaceData(current, m.group(1)))
        return ifaces


class FallbackBackend(IHostBackend):
    name = "unknown"

    def listening_ports(self) -> list[tuple[int, str]]:
        return []

    def unsupported_reason(self) -> str:
        return (f"no listening-port backend for {platform.system() or 'this OS'}; "
                f"enter ports manually")


_BACKENDS = {"Linux": LinuxBackend, "Darwin": MacBackend, "Windows": WindowsBackend}


def backend() -> IHostBackend:
    return _BACKENDS.get(platform.system(), FallbackBackend)()
