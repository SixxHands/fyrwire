from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class Endpoint:

    host: str
    port: int

    def __str__(self) -> str:
        return f"{self.host}:{self.port}"


@dataclass(frozen=True)
class Publication:

    a_name: str
    srv_name: str
    endpoint: Endpoint
    ttl: int = 60
    srv_priority: int = 0
    srv_weight: int = 5

    @property
    def srv_value(self) -> str:
        return f"{self.srv_priority} {self.srv_weight} {self.endpoint.port} {self.a_name}"


class FyrWireError(RuntimeError):
    pass
