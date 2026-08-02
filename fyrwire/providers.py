from __future__ import annotations

import json
import urllib.error
import urllib.request
from abc import ABC, abstractmethod

from .models import FyrWireError, HttpsRecordData, SrvRecordData


class RateLimited(FyrWireError):

    def __init__(self, message: str, retry_after: float | None = None):
        super().__init__(message)
        self.retry_after = retry_after

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
    def ensure_https(self, rec: HttpsRecordData) -> str | None:
        pass

    @abstractmethod
    def delete(self, rtype: str, name: str, expect: str | None = None) -> str | None:
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
            codes = {er.get("code") for er in payload.get("errors", [])}
            if 971 in codes or any("rate" in str(er.get("message", "")).lower()
                                   for er in payload.get("errors", [])):
                raise RateLimited(f"cloudflare rate limit on {method} {path}")
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
        if rtype in ("SRV", "HTTPS"):
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
        if rtype == "HTTPS":
            d = existing.get("data") or {}
            return (str(d.get("target", "")).rstrip(".") == str(content["target"]).rstrip(".")
                    and int(d.get("priority", -1)) == int(content["priority"])
                    and str(d.get("value", "")) == str(content["value"]))
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

    def ensure_https(self, rec: HttpsRecordData) -> str | None:
        what = self._upsert("HTTPS", rec.name, {
            "priority": rec.priority,
            "target": rec.target,
            "value": f"port={rec.port}",
        }, rec.ttl, self.TAG)
        return f"{what} -> {rec.value}" if what else None

    def delete(self, rtype: str, name: str, expect: str | None = None) -> str | None:
        rid, existing = self._find(rtype, name)
        if not rid:
            return None
        comment = (existing or {}).get("comment") or ""
        if self.TAG not in comment:
            raise FyrWireError(
                f"{rtype} {name} is not tagged '{self.TAG}' — refusing to delete "
                f"a record FyrWire did not create")
        if expect is not None:
            data = (existing or {}).get("data") or {}
            actual = str(data.get("target") or existing.get("content") or "")
            if actual.rstrip(".") != expect.rstrip("."):
                raise FyrWireError(
                    f"{rtype} {name} points at {actual!r}, not the expected "
                    f"{expect!r} — refusing to delete a record that is not ours")
        self._call("DELETE", f"/zones/{self.zone}/dns_records/{rid}")
        self._ids.pop((rtype, name), None)
        return f"deleted {rtype} {name}"


@register("desec")
# deSEC enforces a strict per-domain minimum TTL (usually 3600s) and rejects anything lower, so check it upfront.
class DesecProvider(IDnsProvider):

    API = "https://desec.io/api/v1"

    def __init__(self, token: str, domain: str, timeout: int = 10):
        if not token or not domain:
            raise FyrWireError("desec provider needs token and domain")
        self.token = token
        self.domain = domain.strip(".").lower()
        self.timeout = int(timeout)
        self._minimum_ttl: int | None = None

    def _url(self, *parts: str) -> str:
        return f"{self.API}/" + "/".join(p.strip("/") for p in parts) + "/"

    def _call(self, method: str, url: str, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(url, data=data, method=method)
        req.add_header("Authorization", f"Token {self.token}")
        req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as r:
                raw = r.read().decode()
                return json.loads(raw) if raw.strip() else None
        except urllib.error.HTTPError as e:
            detail = e.read().decode(errors="replace")[:300]
            if e.code == 429:
                retry = e.headers.get("Retry-After")
                try:
                    retry = float(retry) if retry else None
                except ValueError:
                    retry = None
                raise RateLimited(
                    f"desec rate limit ({detail})", retry_after=retry) from None
            if e.code == 404:
                return None
            raise FyrWireError(
                f"desec {method} -> HTTP {e.code}: {detail}") from None
        except (urllib.error.URLError, TimeoutError) as e:
            raise FyrWireError(f"desec unreachable: {e}") from None
        except json.JSONDecodeError as e:
            raise FyrWireError(f"desec returned unparseable JSON: {e}") from None

    def minimum_ttl(self) -> int:
        if self._minimum_ttl is None:
            dom = self._call("GET", self._url("domains", self.domain))
            if not dom:
                raise FyrWireError(
                    f"desec does not have a domain named {self.domain!r} on this "
                    f"account — check the name, or add it at desec.io first")
            self._minimum_ttl = int(dom.get("minimum_ttl", 3600))
        return self._minimum_ttl

    def _check_ttl(self, ttl: int) -> None:
        floor = self.minimum_ttl()
        if ttl < floor:
            raise FyrWireError(
                f"desec enforces a minimum TTL of {floor}s on {self.domain}, but "
                f"this node asks for {ttl}s.\n"
                f"  A {floor}s TTL means clients can cache a stale port for that "
                f"long after a rotation.\n"
                f"  Either set the node's ttl to {floor}, or ask deSEC support for "
                f"a lower-TTL exception (they grant them on request).")

    def _subname(self, name: str) -> str:
        n = name.strip(".").lower()
        if n == self.domain:
            return ""
        suffix = "." + self.domain
        if not n.endswith(suffix):
            raise FyrWireError(
                f"{name!r} is not inside {self.domain!r} — the desec provider is "
                f"scoped to one domain, so it cannot write that name")
        return n[: -len(suffix)]

    def _read(self, rtype: str, name: str) -> dict | None:
        sub = self._subname(name)
        return self._call(
            "GET", self._url("domains", self.domain, "rrsets", sub or "@", rtype))

    def _write(self, rrsets: list[dict]) -> None:
        if not rrsets:
            return
        self._call("PATCH", self._url("domains", self.domain, "rrsets"), rrsets)

    @staticmethod
    def _same(existing: dict | None, records: list[str], ttl: int) -> bool:
        if not existing:
            return False
        if int(existing.get("ttl", -1)) != int(ttl):
            return False
        return (sorted(r.lower() for r in existing.get("records", []))
                == sorted(r.lower() for r in records))

    def _ensure(self, rtype: str, name: str, records: list[str],
                ttl: int) -> str | None:
        self._check_ttl(ttl)
        existing = self._read(rtype, name)
        if self._same(existing, records, ttl):
            return None
        verb = "updated" if existing else "created"
        self._write([{"subname": self._subname(name), "type": rtype,
                      "ttl": int(ttl), "records": records}])
        return f"{verb} {rtype} {name}"

    def ensure_a(self, name: str, ip: str, ttl: int) -> str | None:
        what = self._ensure("A", name, [ip], ttl)
        return f"{what} -> {ip}" if what else None

    def ensure_srv(self, rec: SrvRecordData) -> str | None:
        value = f"{rec.priority} {rec.weight} {rec.port} {rec.target.rstrip('.')}."
        what = self._ensure("SRV", rec.name, [value], rec.ttl)
        return f"{what} -> {rec.value}" if what else None

    def ensure_https(self, rec: HttpsRecordData) -> str | None:
        value = (f"{rec.priority} {rec.target.rstrip('.')}. "
                 f"port={rec.port}")
        what = self._ensure("HTTPS", rec.name, [value], rec.ttl)
        return f"{what} -> {rec.value}" if what else None

    def delete(self, rtype: str, name: str, expect: str | None = None) -> str | None:
        if expect is None:
            raise FyrWireError(
                f"refusing to delete {rtype} {name} on desec without an ownership "
                f"witness — desec has no record metadata, so the only safe proof "
                f"is that the record points where FyrWire published")
        existing = self._read(rtype, name)
        if not existing or not existing.get("records"):
            return None
        want = expect.rstrip(".").lower()
        for value in existing["records"]:
            if want in [f.rstrip(".").lower() for f in str(value).split()]:
                break
        else:
            raise FyrWireError(
                f"{rtype} {name} does not point at {expect!r} — refusing to "
                f"delete a record that is not ours")
        sub = self._subname(name)
        self._call("DELETE",
                   self._url("domains", self.domain, "rrsets", sub or "@", rtype))
        return f"deleted {rtype} {name}"
