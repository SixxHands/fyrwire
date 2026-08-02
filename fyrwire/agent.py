from __future__ import annotations

import json
import os
import time
import traceback
from dataclasses import asdict, dataclass, field, fields
from datetime import datetime, timezone

from .models import FyrWireError, HttpsRecordData, NodePlanData, SrvRecordData
from .providers import RateLimited
from .relay import RelayManager, tunnel_address


@dataclass
class AgentState:
    last_host: str | None = None
    last_ports: dict = field(default_factory=dict)
    consecutive_failures: int = 0
    total_publishes: int = 0
    total_failures: int = 0
    history: list = field(default_factory=list)
    alerted_down: bool = False
    alerted_services: list = field(default_factory=list)

    def to_json(self) -> str:
        return json.dumps(asdict(self), indent=2)

    @classmethod
    def load(cls, path: str) -> "AgentState":
        try:
            with open(path) as fh:
                data = json.load(fh)
        except (OSError, json.JSONDecodeError):
            return cls()
        if not isinstance(data, dict):
            return cls()
        known = {f.name for f in fields(cls)}
        return cls(**{k: v for k, v in data.items() if k in known})

    def save(self, path: str) -> None:
        os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
        tmp = f"{path}.tmp"
        with open(tmp, "w") as fh:
            fh.write(self.to_json())
        os.replace(tmp, path)


# Never wipe DNS on a bad cycle. A VPN blip should leave the last working port alone instead of taking everything down.
class Agent:
    def __init__(self, source, provider, plan: NodePlanData, state_path: str, log=print,
                 relay: bool = False, relay_udp: bool = False,
                 udp_options: dict | None = None,
                 bind_host: str | None = None,
                 target_host: str = "127.0.0.1", notifier=None):
        self.source = source
        self.provider = provider
        self.plan = plan
        self.state_path = state_path
        self.state = AgentState.load(state_path)
        self.log = log
        self.target_host = target_host
        self.bind_host = bind_host
        self.relay_udp = relay_udp
        self.relays = (RelayManager(log=log, udp_options=udp_options)
                       if relay else None)
        self.notifier = notifier

    WRITE_SPACING = 0.55

    def _paced(self, fn, *a, **kw):
        gap = time.monotonic() - self._last_write
        if gap < self.WRITE_SPACING:
            time.sleep(self.WRITE_SPACING - gap)
        try:
            return fn(*a, **kw)
        finally:
            self._last_write = time.monotonic()

    def _sync_relays(self, res) -> tuple[list[str], set[str]]:
        failed: set[str] = set()
        if self.relays is None:
            return [], failed
        bind = self.bind_host or tunnel_address()
        if not bind:
            self.log("relay: no tunnel address found; skipping")
            return [], failed

        wanted = {}
        for svc in self.plan.services:
            port = res.ports.get(svc.key)
            if port is None:
                continue
            proto = svc.protocol.lower()
            if proto == "udp" and not self.relay_udp:
                continue
            wanted[svc.key] = (bind, port, self.target_host,
                               svc.internal_port, proto)

        changes = list(self.relays.sync(wanted))
        live = self.relays.stats()
        for key in wanted:
            if key not in live:
                failed.add(key)
        return changes, failed

    def tick(self, force: bool = False) -> dict:
        stamp = datetime.now(timezone.utc).isoformat(timespec="seconds")
        try:
            res = self.source.resolve(self.plan.requests())
        except FyrWireError as e:
            self.state.consecutive_failures += 1
            self.state.total_failures += 1
            self.state.save(self.state_path)
            self.log(f"resolve failed ({self.state.consecutive_failures}x): {e}")
            report = {"ok": False, "at": stamp, "error": str(e),
                      "kept": {"host": self.state.last_host,
                               "ports": dict(self.state.last_ports)}}
            self._alert(report)
            return report

        if self.state.consecutive_failures:
            self.log(f"recovered after {self.state.consecutive_failures} failure(s)")
        self.state.consecutive_failures = 0

        if getattr(self.source, "took_restart", None) and self.source.took_restart():
            force = True

        changes: list[str] = []
        skip = set(res.partial)

        relay_changes, relay_failed = self._sync_relays(res)
        changes.extend(relay_changes)
        skip |= relay_failed
        skipped = sorted(skip)
        self._last_write = 0.0

        if force or res.host != self.state.last_host:
            c = self._paced(self.provider.ensure_a,
                            self.plan.a_name, res.host, self.plan.ttl)
            if c:
                changes.append(c)
            self.state.last_host = res.host

        for svc in self.plan.services:
            port = res.ports.get(svc.key)
            if port is None or svc.key in skip:
                continue
            prev = self.state.last_ports.get(svc.key)
            if not force and prev == port:
                continue
            kind = getattr(svc, "record", "srv")
            if kind == "none":
                self.state.last_ports[svc.key] = port
                continue
            if kind == "https":
                c = self._paced(self.provider.ensure_https, HttpsRecordData(
                    name=svc.srv_name, target=self.plan.a_name, port=port,
                    ttl=svc.ttl or self.plan.ttl))
            else:
                c = self._paced(self.provider.ensure_srv, SrvRecordData(
                    name=svc.srv_name, target=self.plan.a_name, port=port,
                    priority=svc.priority, weight=svc.weight,
                    ttl=svc.ttl or self.plan.ttl))
            if c:
                changes.append(f"[{svc.key}] {c}")
            self.state.last_ports[svc.key] = port


        if changes:
            self.state.total_publishes += 1
            self.state.history = ([{"at": stamp, "host": res.host,
                                    "ports": dict(res.ports), "changes": changes}]
                                  + self.state.history)[:20]
            for c in changes:
                self.log(c)
        if skipped:
            self.log(f"unresolved this cycle (records left alone): {', '.join(skipped)}")

        report = {"ok": True, "at": stamp, "host": res.host,
                  "ports": dict(res.ports), "changes": changes, "skipped": skipped}
        self._alert(report)
        self.state.save(self.state_path)
        return report

    def _alert(self, report: dict) -> None:
        if self.notifier is None:
            return
        try:
            self.notifier.evaluate(report, self.state, self.plan.a_name)
            self.state.save(self.state_path)
        except Exception as e:
            self.log(f"alerting error (ignored): {e}")

    def run(self, interval: int, max_cycles: int | None = None) -> None:
        n = 0
        backoff = 0
        while max_cycles is None or n < max_cycles:
            wait = interval
            try:
                self.tick()
                backoff = 0
            except RateLimited as e:
                backoff = min(backoff * 2 or interval, 900)
                wait = max(backoff, int(getattr(e, "retry_after", 0) or 0))
                self.log(f"rate limited; waiting {wait}s — {e}")
            except FyrWireError as e:
                self.log(f"cycle failed: {e}")
            except Exception as e:
                self.log(f"unexpected error: {e!r}")
                self.log(traceback.format_exc().rstrip())
            n += 1
            if max_cycles is None or n < max_cycles:
                time.sleep(wait)
