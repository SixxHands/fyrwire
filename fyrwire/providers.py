from __future__ import annotations

import json
import urllib.error
import urllib.request
from abc import ABC, abstractmethod

from .models import FyrWireError, SrvRecordData

_REGISTRY: dict[str, type["DnsProvider"]] = {}


def register(name: str):
    def deco(cls):
        cls.name = name
        _REGISTRY[name] = cls
        return cls
    return deco


def build(name: str, opts: dict) -> "DnsProvider":
    try:
        cls = _REGISTRY[name]
    except KeyError:
        raise FyrWireError(
            f"unknown provider {name!r}; available: {', '.join(sorted(_REGISTRY))}"
        ) from None
    return cls(**opts)


def available() -> list[str]:
    return sorted(_REGISTRY)


class IDnsProvider(ABC):
    name: str = "?"

    @abstractmethod
    def ensure_a(self, name: str, ip: str, ttl: int) -> str | None:
        pass

    @abstractmethod
    def ensure_srv(self, rec: SrvRecordData) -> str | None:
        pass

    @abstractmethod
    def delete(self, rtype: str, name: str) -> str | None:
        pass


@register("cloudflare")
# Every record we write gets stamped with our tag. Remove refuses to touch records without it so we never nuke your real DNS.
class CloudflareProvider(IDnsProvider):
    API = "https://api.cloudflare.com/client/v4"

    def __init__(self, api_token: str, zone_id: str, timeout: int = 10):
        if not api_token or not zone_id:
            raise FyrWireError("cloudflare provider needs api_token and zone_id")
        self.token, self.zone, self.timeout = api_token, zone_id, int(timeout)
        self._ids: dict[tuple[str, str], str] = {}

    def _call(self, method: str, path: str, body: dict | None = None) -> dict:
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(f"{self.API}{path}", data=data, method=method)
        req.add_header("Authorization", f"Bearer {self.token}")
        req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as r:
                payload = json.loads(r.read().decode())
        except urllib.error.HTTPError as e:
            detail = e.read().decode(errors="replace")[:300]
            raise FyrWireError(f"cloudflare {method} {path} -> HTTP {e.code}: {detail}") from None
        except (urllib.error.URLError, TimeoutError) as e:
            raise FyrWireError(f"cloudflare unreachable: {e}") from None
        except json.JSONDecodeError as e:
            raise FyrWireError(f"cloudflare returned unparseable JSON: {e}") from None

        if not payload.get("success", False):
            errs = "; ".join(
                f"{er.get('code')}: {er.get('message')}" for er in payload.get("errors", [])
            ) or "unknown error"
            raise FyrWireError(f"cloudflare rejected {method} {path}: {errs}")
        return payload

    def _find(self, rtype: str, name: str) -> tuple[str | None, dict | None]:
        res = self._call("GET", f"/zones/{self.zone}/dns_records?type={rtype}&name={name}")
        for rec in res.get("result", []):
            return rec["id"], rec
        return None, None

    def _upsert(self, rtype: str, name: str, content, ttl: int, comment: str) -> str | None:
        key = (rtype, name)
        rid = self._ids.get(key)
        existing = None
        if rid:
            try:
                existing = self._call("GET", f"/zones/{self.zone}/dns_records/{rid}")["result"]
            except FyrWireError:
                self._ids.pop(key, None)
                rid = None
        if not rid:
            rid, existing = self._find(rtype, name)
            if rid:
                self._ids[key] = rid

        body: dict = {"type": rtype, "name": name, "ttl": ttl, "comment": comment}
        if rtype == "SRV":
            body["data"] = content
        else:
            body["content"] = content

        if rid and existing and self._matches(rtype, existing, content, ttl):
            return None

        if rid:
            self._call("PUT", f"/zones/{self.zone}/dns_records/{rid}", body)
            return f"updated {rtype} {name}"
        created = self._call("POST", f"/zones/{self.zone}/dns_records", body)
        self._ids[key] = created["result"]["id"]
        return f"created {rtype} {name}"

    @staticmethod
    def _matches(rtype: str, existing: dict, content, ttl: int) -> bool:
        if existing.get("ttl") != ttl:
            return False
        if rtype == "SRV":
            d = existing.get("data") or {}
            return (int(d.get("port", -1)) == int(content["port"])
                    and str(d.get("target", "")).rstrip(".") == str(content["target"]).rstrip(".")
                    and int(d.get("priority", -1)) == int(content["priority"])
                    and int(d.get("weight", -1)) == int(content["weight"]))
        return str(existing.get("content", "")) == str(content)

    TAG = "managed by fyrwire"

    def ensure_a(self, name: str, ip: str, ttl: int) -> str | None:
        what = self._upsert("A", name, ip, ttl, self.TAG)
        return f"{what} -> {ip}" if what else None

    def ensure_srv(self, rec: SrvRecordData) -> str | None:
        what = self._upsert("SRV", rec.name, {
            "priority": rec.priority,
            "weight": rec.weight,
            "port": rec.port,
            "target": rec.target,
        }, rec.ttl, self.TAG)
        return f"{what} -> {rec.value}" if what else None

    def delete(self, rtype: str, name: str) -> str | None:
        rid, existing = self._find(rtype, name)
        if not rid:
            return None
        comment = (existing or {}).get("comment") or ""
        if self.TAG not in comment:
            raise FyrWireError(
                f"{rtype} {name} is not tagged '{self.TAG}' — refusing to delete "
                f"a record FyrWire did not create")
        self._call("DELETE", f"/zones/{self.zone}/dns_records/{rid}")
        self._ids.pop((rtype, name), None)
        return f"deleted {rtype} {name}"
