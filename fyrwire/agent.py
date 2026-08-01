from __future__ import annotations

import json
import os
import time
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone

from .models import FyrWireError, NodePlanData, SrvRecordData


@dataclass
class AgentState:
    last_host: str | None = None
    last_ports: dict = field(default_factory=dict)
    consecutive_failures: int = 0
    total_publishes: int = 0
    total_failures: int = 0
    history: list = field(default_factory=list)

    def to_json(self) -> str:
        return json.dumps(asdict(self), indent=2)

    @classmethod
    def load(cls, path: str) -> "AgentState":
        try:
            with open(path) as fh:
                return cls(**json.load(fh))
        except (OSError, json.JSONDecodeError, TypeError):
            return cls()

    def save(self, path: str) -> None:
        os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
        tmp = f"{path}.tmp"
        with open(tmp, "w") as fh:
            fh.write(self.to_json())
        os.replace(tmp, path)


# Never wipe DNS on a bad cycle. A VPN blip should leave the last working port alone instead of taking everything down.
class Agent:
    def __init__(self, source, provider, plan: NodePlanData, state_path: str, log=print):
        self.source = source
        self.provider = provider
        self.plan = plan
        self.state_path = state_path
        self.state = AgentState.load(state_path)
        self.log = log

    def tick(self, force: bool = False) -> dict:
        stamp = datetime.now(timezone.utc).isoformat(timespec="seconds")
        try:
            res = self.source.resolve(self.plan.requests())
        except FyrWireError as e:
            self.state.consecutive_failures += 1
            self.state.total_failures += 1
            self.state.save(self.state_path)
            self.log(f"resolve failed ({self.state.consecutive_failures}x): {e}")
            return {"ok": False, "at": stamp, "error": str(e),
                    "kept": {"host": self.state.last_host, "ports": dict(self.state.last_ports)}}

        if self.state.consecutive_failures:
            self.log(f"recovered after {self.state.consecutive_failures} failure(s)")
        self.state.consecutive_failures = 0

        changes: list[str] = []
        skipped = sorted(res.partial)

        if force or res.host != self.state.last_host:
            c = self.provider.ensure_a(self.plan.a_name, res.host, self.plan.ttl)
            if c:
                changes.append(c)
            self.state.last_host = res.host

        for svc in self.plan.services:
            port = res.ports.get(svc.key)
            if port is None:
                continue
            prev = self.state.last_ports.get(svc.key)
            if not force and prev == port:
                continue
            rec = SrvRecordData(name=svc.srv_name, target=self.plan.a_name, port=port,
                            priority=svc.priority, weight=svc.weight,
                            ttl=svc.ttl or self.plan.ttl)
            c = self.provider.ensure_srv(rec)
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

        self.state.save(self.state_path)
        return {"ok": True, "at": stamp, "host": res.host, "ports": dict(res.ports),
                "changes": changes, "skipped": skipped}

    def run(self, interval: int, max_cycles: int | None = None) -> None:
        n = 0
        while max_cycles is None or n < max_cycles:
            self.tick()
            n += 1
            if max_cycles is None or n < max_cycles:
                time.sleep(interval)
