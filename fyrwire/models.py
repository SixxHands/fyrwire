from __future__ import annotations

from dataclasses import dataclass, field


class FyrWireError(RuntimeError):
    pass


@dataclass(frozen=True)
class PortRequestData:

    key: str
    internal_port: int
    protocol: str = "tcp"

    def __str__(self) -> str:
        return f"{self.key}({self.protocol}/{self.internal_port})"


@dataclass(frozen=True)
class EndpointData:

    host: str
    ports: dict[str, int]
    partial: set[str] = field(default_factory=set)

    def endpoint(self, key: str) -> str:
        return f"{self.host}:{self.ports[key]}"

    def endpoint_key(self) -> str:
        inner = ",".join(f"{k}={self.ports[k]}" for k in sorted(self.ports))
        return f"{self.host}|{inner}"


@dataclass(frozen=True)
class Service:

    key: str
    internal_port: int
    srv_name: str
    protocol: str = "tcp"
    priority: int = 0
    weight: int = 5
    ttl: int | None = None

    def request(self) -> PortRequestData:
        return PortRequestData(self.key, self.internal_port, self.protocol)


@dataclass(frozen=True)
class NodePlanData:

    a_name: str
    services: tuple[Service, ...]
    ttl: int = 60

    def requests(self) -> list[PortRequestData]:
        return [s.request() for s in self.services]

    def service(self, key: str) -> Service:
        for s in self.services:
            if s.key == key:
                return s
        raise KeyError(key)


@dataclass(frozen=True)
class SrvRecordData:
    name: str
    target: str
    port: int
    priority: int
    weight: int
    ttl: int

    @property
    def value(self) -> str:
        return f"{self.priority} {self.weight} {self.port} {self.target}"
