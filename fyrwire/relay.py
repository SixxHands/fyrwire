from __future__ import annotations

import asyncio
import socket
import threading

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

    @property
    def signature(self) -> tuple:
        return (self.bind_host, self.bind_port, self.target_host, self.target_port)

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

    def __init__(self, log=print):
        self.log = log
        self._listeners: dict[str, _Listener] = {}
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
            bh, bp, th, tp = spec
            lis = _Listener(key, bh, bp, th, tp, log=self.log)
            try:
                self._call(lis.start())
            except OSError as e:
                changes.append(f"relay {key} FAILED to bind {bh}:{bp} — {e}")
                continue
            self._listeners[key] = lis
            verb = "rebound" if cur is not None else "listening"
            changes.append(f"relay {key} {verb} {bh}:{bp} → {th}:{tp}")
        return changes

    def stats(self) -> dict[str, dict]:
        return {k: {"bind": f"{l.bind_host}:{l.bind_port}",
                    "target": f"{l.target_host}:{l.target_port}",
                    "active": l.active, "total": l.total, "refused": l.refused}
                for k, l in self._listeners.items()}

    def shutdown(self) -> None:
        for key in list(self._listeners):
            try:
                self._call(self._listeners.pop(key).stop())
            except Exception:
                pass
        self._loop.call_soon_threadsafe(self._loop.stop)


def tunnel_address(interface_hint: str | None = None) -> str | None:
    import json
    import subprocess
    try:
        out = subprocess.run(["ip", "-j", "-4", "addr", "show"],
                             capture_output=True, text=True, timeout=5).stdout
        links = json.loads(out or "[]")
    except Exception:
        return None
    best = None
    for link in links:
        name = link.get("ifname", "")
        if name == "lo":
            continue
        for a in link.get("addr_info", []):
            ip, plen = a.get("local"), a.get("prefixlen")
            if not ip:
                continue
            if interface_hint and name == interface_hint:
                return ip
            if plen == 32 or name.startswith(("wg", "tun", "proton")):
                best = best or ip
    return best
