from __future__ import annotations

import json
import os
import time
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone

from .models import Endpoint, FyrWireError, Publication


@dataclass
class AgentState:
    last_endpoint: str | None = None
    last_published: str | None = None
    consecutive_failures: int = 0
    total_publishes: int = 0
    total_failures: int = 0
    history: list[dict] = field(default_factory=list)

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
    def __init__(self, source, provider, plan, state_path, log=print):
        self.source = source
        self.provider = provider
        self.plan = plan
        self.state_path = state_path
        self.state = AgentState.load(state_path)
        self.log = log

    def _publication(self, ep: Endpoint) -> Publication:
        return Publication(
            a_name=self.plan["a_name"],
            srv_name=self.plan["srv_name"],
            endpoint=ep,
            ttl=int(self.plan.get("ttl", 60)),
            srv_priority=int(self.plan.get("srv_priority", 0)),
            srv_weight=int(self.plan.get("srv_weight", 5)),
        )

    def tick(self, force: bool = False) -> dict:
        stamp = datetime.now(timezone.utc).isoformat(timespec="seconds")
        try:
            ep = self.source.discover()
        except FyrWireError as e:
            self.state.consecutive_failures += 1
            self.state.total_failures += 1
            self.state.save(self.state_path)
            self.log(f"discover failed ({self.state.consecutive_failures}x): {e}")
            return {"ok": False, "at": stamp, "error": str(e),
                    "kept": self.state.last_published}

        if self.state.consecutive_failures:
            self.log(f"recovered after {self.state.consecutive_failures} failure(s)")
        self.state.consecutive_failures = 0

        changed = (str(ep) != self.state.last_endpoint)
        if not changed and not force:
            self.state.last_endpoint = str(ep)
            self.state.save(self.state_path)
            return {"ok": True, "at": stamp, "endpoint": str(ep), "changes": []}

        pub = self._publication(ep)
        changes = self.provider.publish(pub)
        self.state.last_endpoint = str(ep)
        if changes:
            self.state.last_published = str(ep)
            self.state.total_publishes += 1
            self.state.history = ([{"at": stamp, "endpoint": str(ep), "changes": changes}]
                                  + self.state.history)[:20]
            for c in changes:
                self.log(c)
        self.state.save(self.state_path)
        return {"ok": True, "at": stamp, "endpoint": str(ep), "changes": changes}

    def run(self, interval: int, max_cycles: int | None = None) -> None:
        n = 0
        while max_cycles is None or n < max_cycles:
            self.tick()
            n += 1
            if max_cycles is None or n < max_cycles:
                time.sleep(interval)
