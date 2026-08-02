from __future__ import annotations

import json
import re
import time
import urllib.error
import urllib.request
from abc import ABC, abstractmethod
from urllib.parse import quote

from .models import FyrWireError, HttpsRecordData, SrvRecordData


class RateLimited(FyrWireError):

    def __init__(self, message: str, retry_after: float | None = None):
        super().__init__(message)
        self.retry_after = retry_after

_IPV4 = re.compile(r"^\d{1,3}(\.\d{1,3}){3}\.?$")


def _check_target(target: str) -> None:
    t = target.strip().rstrip(".")
    if not t:
        raise FyrWireError("SRV/HTTPS target is empty")
    if _IPV4.match(target.strip()):
        raise FyrWireError(
            f"{target!r} is an IP address, but an SRV/HTTPS target must be a "
            f"hostname. Point node.a_name at a NAME whose A record holds the "
            f"address — an IP here produces a record that resolves fine and "
            f"then fails in the client.")
    if "_" in t:
        raise FyrWireError(
            f"{target!r} contains an underscore. That is legal in the SRV "
            f"NAME (_minecraft._tcp....) but not in its TARGET, and deSEC "
            f"rejects it with a parse error that names neither.")


def _target_of(rtype: str, value: str) -> str | None:
    fields = value.split()
    if rtype == "SRV":
        return fields[3] if len(fields) >= 4 else None
    if rtype in ("HTTPS", "SVCB"):
        return fields[1] if len(fields) >= 2 else None
    if rtype in ("A", "AAAA", "CNAME"):
        return fields[0] if fields else None
    return None


def _flatten_desec_error(detail: str) -> str:
    try:
        obj = json.loads(detail)
    except (ValueError, TypeError):
        return detail.strip()[:200]
    parts: list[str] = []

    def walk(o):
        if isinstance(o, str):
            parts.append(o)
        elif isinstance(o, list):
            for i in o:
                walk(i)
        elif isinstance(o, dict):
            for k, v in o.items():
                if k == "detail":
                    walk(v)
                else:
                    before = len(parts)
                    walk(v)
                    for i in range(before, len(parts)):
                        parts[i] = f"{k}: {parts[i]}"

    walk(obj)
    return "; ".join(parts)[:300] or detail.strip()[:200]


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
        self._zone_name: str | None = None
        self._zone_checked = False

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

    def _check_zone(self, name: str) -> None:
        if not self._zone_checked:
            self._zone_checked = True
            res = self._call("GET", f"/zones/{self.zone}").get("result") or {}
            self._zone_name = (res.get("name") or "").lower() or None
            status = res.get("status")
            if status and status != "active":
                raise FyrWireError(
                    f"cloudflare zone {self._zone_name} is {status!r}, not "
                    f"active — records written to it will be accepted and will "
                    f"never resolve until the nameservers are delegated")
        if not self._zone_name:
            return
        n = name.rstrip(".").lower()
        if n != self._zone_name and not n.endswith("." + self._zone_name):
            raise FyrWireError(
                f"{name!r} is not inside the zone {self._zone_name!r} that "
                f"zone_id points at — the record would be accepted and would "
                f"never resolve")

    def _upsert(self, rtype: str, name: str, content, ttl: int, comment: str) -> str | None:
        self._check_zone(name)
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

        if rid and existing and self._matches(rtype, existing, content, ttl, name):
            return None

        if rid:
            self._call("PUT", f"/zones/{self.zone}/dns_records/{rid}", body)
            return f"updated {rtype} {name}"
        created = self._call("POST", f"/zones/{self.zone}/dns_records", body)
        self._ids[key] = created["result"]["id"]
        return f"created {rtype} {name}"

    @staticmethod
    def _matches(rtype: str, existing: dict, content, ttl: int,
                 name: str | None = None) -> bool:
        if existing.get("type") not in (None, rtype):
            return False
        if name is not None:
            got = str(existing.get("name") or "").rstrip(".").lower()
            if got and got != name.rstrip(".").lower():
                return False
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
            if actual.rstrip(".").lower() != expect.rstrip(".").lower():
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
        self.domain = domain.strip().strip(".").lower()
        self.timeout = int(timeout)
        self._minimum_ttl: int | None = None
        self._min_ttl_at = 0.0

    def _url(self, *parts: str) -> str:
        clean = [quote(p.strip(), safe="@*") for p in parts]
        return f"{self.API}/" + "/".join(clean) + "/"

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
            if e.code == 404 and method == "GET":
                return None
            if e.code == 404 and method == "DELETE":
                return None
            msg = _flatten_desec_error(detail)
            if e.code in (401, 403):
                raise FyrWireError(
                    f"desec refused the token (HTTP {e.code}): {msg}. This is a "
                    f"permissions problem, not a transient one — retrying will "
                    f"not help.") from None
            if e.code in (409, 500, 502, 503, 504):
                raise RateLimited(
                    f"desec temporary failure (HTTP {e.code}): {msg}") from None
            raise FyrWireError(
                f"desec {method} -> HTTP {e.code}: {msg}") from None
        except (urllib.error.URLError, TimeoutError) as e:
            raise FyrWireError(f"desec unreachable: {e}") from None
        except json.JSONDecodeError as e:
            raise FyrWireError(f"desec returned unparseable JSON: {e}") from None

    MIN_TTL_CACHE_SECONDS = 3600

    def minimum_ttl(self, force: bool = False) -> int:
        now = time.monotonic()
        if (force or self._minimum_ttl is None
                or now - self._min_ttl_at > self.MIN_TTL_CACHE_SECONDS):
            dom = self._call("GET", self._url("domains", self.domain))
            if not dom:
                raise FyrWireError(
                    f"desec does not have a domain named {self.domain!r} on this "
                    f"account — check the name, or add it at desec.io first")
            raw = dom.get("minimum_ttl")
            if raw is None:
                raise FyrWireError(
                    f"desec did not report a minimum_ttl for {self.domain} — "
                    f"the API may have changed. Refusing to guess.")
            self._minimum_ttl = int(raw)
            self._min_ttl_at = now
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
        n = name.strip().strip(".").lower()
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
    def _canon(rtype: str, value: str) -> str:
        fields = value.split()
        if rtype in ("HTTPS", "SVCB") and len(fields) >= 2:
            params = sorted(f.replace('"', "") for f in fields[2:])
            return " ".join([fields[0], fields[1].rstrip(".").lower()] + params)
        if rtype == "SRV" and len(fields) >= 4:
            return " ".join(fields[:3] + [fields[3].rstrip(".").lower()])
        return value.strip().lower()

    @classmethod
    def _same(cls, rtype: str, existing: dict | None, records: list[str],
              ttl: int) -> bool:
        if not existing:
            return False
        if int(existing.get("ttl", -1)) != int(ttl):
            return False
        return (sorted(cls._canon(rtype, str(r)) for r in existing.get("records", []))
                == sorted(cls._canon(rtype, r) for r in records))

    def _ensure(self, rtype: str, name: str, records: list[str],
                ttl: int) -> str | None:
        self._check_ttl(ttl)
        existing = self._read(rtype, name)
        if self._same(rtype, existing, records, ttl):
            return None
        verb = "updated" if existing else "created"
        self._write([{"subname": self._subname(name), "type": rtype,
                      "ttl": int(ttl), "records": records}])
        return f"{verb} {rtype} {name}"

    def ensure_a(self, name: str, ip: str, ttl: int) -> str | None:
        what = self._ensure("A", name, [ip], ttl)
        return f"{what} -> {ip}" if what else None

    def ensure_srv(self, rec: SrvRecordData) -> str | None:
        _check_target(rec.target)
        value = f"{rec.priority} {rec.weight} {rec.port} {rec.target.rstrip('.')}."
        what = self._ensure("SRV", rec.name, [value], rec.ttl)
        return f"{what} -> {rec.value}" if what else None

    def ensure_https(self, rec: HttpsRecordData) -> str | None:
        _check_target(rec.target)
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
        records = (existing or {}).get("records") or []
        if not records:
            return None
        if len(records) > 1:
            raise FyrWireError(
                f"{rtype} {name} holds {len(records)} records, and deSEC can "
                f"only delete the whole set. Removing ours would take the "
                f"others with it, so this has to be done by hand:\n  "
                + "\n  ".join(str(r) for r in records))
        want = expect.rstrip(".").lower()
        actual = _target_of(rtype, str(records[0]))
        if actual is None or actual.rstrip(".").lower() != want:
            raise FyrWireError(
                f"{rtype} {name} points at {actual!r}, not the expected "
                f"{expect!r} — refusing to delete a record that is not ours")
        sub = self._subname(name)
        self._call("DELETE",
                   self._url("domains", self.domain, "rrsets", sub or "@", rtype))
        return f"deleted {rtype} {name}"
