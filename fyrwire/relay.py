from __future__ import annotations

import asyncio
import socket
import sys
import threading
import time
from dataclasses import dataclass

BUF = 64 * 1024


class _Listener:

    def __init__(self, key: str, bind_host: str, bind_port: int,
                 target_host: str, target_port: int, max_conns: int = 64,
                 connect_timeout: float = 5.0, log=print):
        self.key = key
        self.bind_host, self.bind_port = bind_host, bind_port
        self.target_host, self.target_port = target_host, target_port
        self.max_conns = max_conns
        self.connect_timeout = connect_timeout
        self.log = log
        self.server: asyncio.AbstractServer | None = None
        self.active = 0
        self.total = 0
        self.refused = 0

    proto = "tcp"

    @property
    def signature(self) -> tuple:
        return (self.bind_host, self.bind_port,
                self.target_host, self.target_port, self.proto)

    async def _pump(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        try:
            while True:
                data = await reader.read(BUF)
                if not data:
                    break
                writer.write(data)
                await writer.drain()
        except (ConnectionResetError, BrokenPipeError, asyncio.IncompleteReadError):
            pass
        finally:
            try:
                writer.close()
            except Exception:
                pass

    async def _handle(self, c_reader, c_writer) -> None:
        if self.active >= self.max_conns:
            self.refused += 1
            c_writer.close()
            return
        self.active += 1
        self.total += 1
        s_writer = None
        try:
            s_reader, s_writer = await asyncio.wait_for(
                asyncio.open_connection(self.target_host, self.target_port),
                timeout=self.connect_timeout)
            for w in (c_writer, s_writer):
                sock = w.get_extra_info("socket")
                if sock is not None:
                    sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
            await asyncio.gather(self._pump(c_reader, s_writer),
                                 self._pump(s_reader, c_writer))
        except (asyncio.TimeoutError, ConnectionRefusedError, OSError):
            pass
        finally:
            self.active -= 1
            for w in (c_writer, s_writer):
                if w is not None:
                    try:
                        w.close()
                    except Exception:
                        pass

    async def start(self) -> None:
        self.server = await asyncio.start_server(
            self._handle, self.bind_host, self.bind_port,
            reuse_address=True, backlog=128)

    async def stop(self) -> None:
        if self.server is not None:
            self.server.close()
            try:
                await self.server.wait_closed()
            except Exception:
                pass
            self.server = None


# Close the listener and kick open sockets on rebind so port rotation never deadlocks waiting on an idle player.
class RelayManager:

    def __init__(self, log=print, udp_options: dict | None = None):
        self.log = log
        self.udp_options = udp_options or {}
        self._listeners: dict = {}
        self._loop = asyncio.new_event_loop()
        self._thread = threading.Thread(target=self._run, daemon=True,
                                        name="fyrwire-relay")
        self._thread.start()

    def _run(self) -> None:
        asyncio.set_event_loop(self._loop)
        self._loop.run_forever()

    def _call(self, coro):
        return asyncio.run_coroutine_threadsafe(coro, self._loop).result(timeout=15)

    def sync(self, wanted: dict[str, tuple]) -> list[str]:
        wanted = {k: (v if len(v) == 5 else (*v, "tcp")) for k, v in wanted.items()}
        changes: list[str] = []

        for key in list(self._listeners):
            if key not in wanted:
                self._call(self._listeners.pop(key).stop())
                changes.append(f"relay {key} stopped")

        for key, spec in wanted.items():
            cur = self._listeners.get(key)
            if cur is not None and cur.signature == spec:
                continue
            if cur is not None:
                self._call(cur.stop())
            bh, bp, th, tp, proto = spec
            cls = _UdpListener if proto == "udp" else _Listener
            kw = dict(self.udp_options) if proto == "udp" else {}
            lis = cls(key, bh, bp, th, tp, log=self.log, **kw)
            try:
                self._call(lis.start())
            except OSError as e:
                changes.append(f"relay {key} FAILED to bind {bh}:{bp}/{proto} — {e}")
                continue
            self._listeners[key] = lis
            verb = "rebound" if cur is not None else "listening"
            changes.append(f"relay {key} {verb} {bh}:{bp} → {th}:{tp} ({proto})")
        return changes

    def stats(self) -> dict[str, dict]:
        out = {}
        for k, l in self._listeners.items():
            if hasattr(l, "stats"):
                out[k] = l.stats()
            else:
                out[k] = {"proto": "tcp",
                          "bind": f"{l.bind_host}:{l.bind_port}",
                          "target": f"{l.target_host}:{l.target_port}",
                          "active": l.active, "total": l.total,
                          "refused": l.refused}
        return out

    def shutdown(self) -> None:
        for key in list(self._listeners):
            try:
                self._call(self._listeners.pop(key).stop())
            except Exception:
                pass
        self._loop.call_soon_threadsafe(self._loop.stop)


def tunnel_address(interface_hint: str | None = None) -> str | None:
    from . import hostinfo

    if interface_hint:
        for iface in hostinfo.backend().interfaces():
            if iface.name == interface_hint:
                return iface.address
    return hostinfo.egress_address()


UDP_IDLE_NEW = 30
UDP_IDLE_ESTABLISHED = 180
UDP_REAP_INTERVAL = 5
UDP_MAX_SESSIONS = 512
UDP_MAX_PER_IP = 8
UDP_NEW_PER_SEC = 32
UDP_STOP_TIMEOUT = 5.0


@dataclass
class _UdpSession:

    client: tuple
    sock: socket.socket
    transport: asyncio.DatagramTransport | None = None
    proto: object = None
    last_rx: float = 0.0
    last_tx: float = 0.0
    replied: bool = False
    c2s_bytes: int = 0
    s2c_bytes: int = 0


class _UdpListenerProtocol(asyncio.DatagramProtocol):
    def __init__(self, owner: "_UdpListener"):
        self.owner = owner
        self.lost = owner.loop.create_future()

    def datagram_received(self, data: bytes, addr) -> None:
        self.owner.on_client(data, addr)

    def error_received(self, exc) -> None:
        self.owner.on_listener_error(exc)

    def connection_lost(self, exc) -> None:
        if not self.lost.done():
            self.lost.set_result(True)


class _UdpUpstreamProtocol(asyncio.DatagramProtocol):
    def __init__(self, owner: "_UdpListener", session: _UdpSession):
        self.owner = owner
        self.session = session
        self.lost = owner.loop.create_future()

    def datagram_received(self, data: bytes, addr) -> None:
        self.owner.on_backend(self.session, data)

    def error_received(self, exc) -> None:
        self.owner.on_upstream_error(self.session, exc)

    def connection_lost(self, exc) -> None:
        if not self.lost.done():
            self.lost.set_result(True)


class _UdpListener:

    proto = "udp"

    def __init__(self, key: str, bind_host: str, bind_port: int,
                 target_host: str, target_port: int, log=print,
                 idle_new: int = UDP_IDLE_NEW,
                 idle_established: int = UDP_IDLE_ESTABLISHED,
                 max_sessions: int = UDP_MAX_SESSIONS,
                 max_per_ip: int = UDP_MAX_PER_IP,
                 new_per_sec: float = UDP_NEW_PER_SEC,
                 reap_interval: float = UDP_REAP_INTERVAL,
                 drop_zero_length: bool = True,
                 now=time.monotonic):
        self.key = key
        self.bind_host, self.bind_port = bind_host, bind_port
        self.target_host, self.target_port = target_host, target_port
        self.log = log
        self.idle_new = idle_new
        self.idle_established = idle_established
        self.max_per_ip = max_per_ip
        self.new_per_sec = float(new_per_sec)
        self.reap_interval = reap_interval
        self.drop_zero_length = drop_zero_length
        self.now = now

        self.max_sessions = self._clamp(max_sessions)
        self.sessions: dict[tuple, _UdpSession] = {}
        self.per_ip: dict[str, int] = {}
        self.loop: asyncio.AbstractEventLoop | None = None
        self.transport: asyncio.DatagramTransport | None = None
        self.protocol: _UdpListenerProtocol | None = None
        self.backend: tuple | None = None
        self._reaper: asyncio.Task | None = None
        self._tokens = float(new_per_sec)
        self._tokens_at = 0.0

        self.total = 0
        self.dropped = 0
        self.upstream_errors = 0
        self.open_failed = 0

    @staticmethod
    def _clamp(cap: int) -> int:
        try:
            import resource
            soft = resource.getrlimit(resource.RLIMIT_NOFILE)[0]
            if soft > 0:
                cap = min(cap, max(16, soft - 128))
        except (ImportError, ValueError, OSError):
            pass
        try:
            with open("/proc/sys/net/ipv4/ip_local_port_range") as fh:
                lo, hi = (int(x) for x in fh.read().split())
            cap = min(cap, hi - lo)
        except (OSError, ValueError):
            pass
        return max(1, cap)

    def _allow_new(self) -> bool:
        now = self.now()
        if self._tokens_at == 0.0:
            self._tokens_at = now
        self._tokens = min(self.new_per_sec,
                           self._tokens + (now - self._tokens_at) * self.new_per_sec)
        self._tokens_at = now
        if self._tokens < 1.0:
            return False
        self._tokens -= 1.0
        return True

    @property
    def signature(self) -> tuple:
        return (self.bind_host, self.bind_port,
                self.target_host, self.target_port, self.proto)

    async def start(self) -> None:
        self.loop = asyncio.get_running_loop()
        infos = socket.getaddrinfo(self.target_host, self.target_port,
                                   socket.AF_INET, socket.SOCK_DGRAM)
        self.backend = infos[0][4]
        self.transport, self.protocol = await self.loop.create_datagram_endpoint(
            lambda: _UdpListenerProtocol(self),
            local_addr=(self.bind_host, self.bind_port),
            family=socket.AF_INET)
        self._reaper = self.loop.create_task(self._reap())

    async def stop(self) -> None:
        if self._reaper is not None:
            self._reaper.cancel()
            self._reaper = None
        waits = []
        for s in list(self.sessions.values()):
            if s.transport is not None:
                waits.append(s.proto.lost)
                s.transport.close()
            else:
                s.sock.close()
        self.sessions.clear()
        self.per_ip.clear()
        if self.transport is not None:
            waits.append(self.protocol.lost)
            self.transport.close()
        if waits:
            try:
                await asyncio.wait_for(
                    asyncio.gather(*waits, return_exceptions=True),
                    timeout=UDP_STOP_TIMEOUT)
            except (asyncio.TimeoutError, asyncio.CancelledError):
                self.log(f"udp relay {self.key}: teardown timed out; "
                         f"rebinding this port may fail")
        self.transport = None
        self.protocol = None

    def _open(self, addr) -> _UdpSession | None:
        u = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        u.setblocking(False)
        try:
            u.connect(self.backend)
        except OSError:
            u.close()
            self.open_failed += 1
            return None
        now = self.now()
        s = _UdpSession(client=addr, sock=u, last_rx=now, last_tx=now)
        self.sessions[addr] = s
        self.per_ip[addr[0]] = self.per_ip.get(addr[0], 0) + 1
        self.total += 1
        self.loop.create_task(self._attach(s))
        return s

    async def _attach(self, s: _UdpSession) -> None:
        proto = _UdpUpstreamProtocol(self, s)
        try:
            t, _ = await self.loop.create_datagram_endpoint(
                lambda: proto, sock=s.sock)
        except OSError:
            self._evict(s, "attach failed")
            return
        if self.sessions.get(s.client) is not s:
            t.close()
            return
        s.transport, s.proto = t, proto

    def _evict(self, s: _UdpSession, reason: str = "") -> None:
        if self.sessions.get(s.client) is s:
            del self.sessions[s.client]
            n = self.per_ip.get(s.client[0], 1) - 1
            if n <= 0:
                self.per_ip.pop(s.client[0], None)
            else:
                self.per_ip[s.client[0]] = n
        if s.transport is not None:
            s.transport.close()
        else:
            try:
                s.sock.close()
            except OSError:
                pass

    async def _reap(self) -> None:
        while True:
            try:
                await asyncio.sleep(self.reap_interval)
            except asyncio.CancelledError:
                return
            self.reap_once()

    def reap_once(self) -> int:
        now = self.now()
        gone = 0
        for s in list(self.sessions.values()):
            idle = now - max(s.last_rx, s.last_tx)
            limit = self.idle_established if s.replied else self.idle_new
            if idle > limit:
                self._evict(s, "idle")
                gone += 1
        return gone

    def on_client(self, data: bytes, addr) -> None:
        if not data and self.drop_zero_length:
            return
        s = self.sessions.get(addr)
        if s is None:
            if (len(self.sessions) >= self.max_sessions
                    or self.per_ip.get(addr[0], 0) >= self.max_per_ip
                    or not self._allow_new()):
                self.dropped += 1
                return
            s = self._open(addr)
            if s is None:
                return
        s.last_rx = self.now()
        s.c2s_bytes += len(data)
        self._send_upstream(s, data)

    def _send_upstream(self, s: _UdpSession, data: bytes) -> None:
        try:
            if s.transport is not None:
                s.transport.sendto(data)
            else:
                s.sock.send(data)
        except BlockingIOError:
            self.dropped += 1
        except OSError as e:
            self.upstream_errors += 1
            self._evict(s, f"send errno {e.errno}")

    def on_backend(self, s: _UdpSession, data: bytes) -> None:
        if self.transport is None or self.sessions.get(s.client) is not s:
            return
        s.replied = True
        s.last_tx = self.now()
        s.s2c_bytes += len(data)
        try:
            self.transport.sendto(data, s.client)
        except OSError:
            pass

    def on_upstream_error(self, s: _UdpSession, exc) -> None:
        self.upstream_errors += 1
        self._evict(s, f"upstream {exc}")

    def on_listener_error(self, exc) -> None:
        self.upstream_errors += 1
        if sys.platform == "win32":
            self.log(f"udp relay {self.key}: listener error on Windows ({exc}); "
                     f"rebuilding, as the transport may have gone deaf")
            if self.loop is not None:
                self.loop.create_task(self._rebuild())

    async def _rebuild(self) -> None:
        try:
            await self.stop()
            await self.start()
        except OSError as e:
            self.log(f"udp relay {self.key}: rebuild failed — {e}")

    def stats(self) -> dict:
        return {"proto": "udp",
                "bind": f"{self.bind_host}:{self.bind_port}",
                "target": f"{self.target_host}:{self.target_port}",
                "sessions": len(self.sessions),
                "sessions_total": self.total,
                "sessions_dropped": self.dropped,
                "upstream_errors": self.upstream_errors,
                "open_failed": self.open_failed,
                "c2s_bytes": sum(s.c2s_bytes for s in self.sessions.values()),
                "s2c_bytes": sum(s.s2c_bytes for s in self.sessions.values())}
