from __future__ import annotations

import difflib
import inspect
import types
import typing

from .models import FyrWireError

SCHEMA_VERSION = 1

NODE_KEYS = {
    "a_name": (str, True),
    "ttl": (int, False),
    "relay": (bool, False),
    "relay_udp": (bool, False),
    "bind_host": (str, False),
    "target_host": (str, False),
    "udp": (dict, False),
}

UDP_KEYS = {
    "idle_new": (int, False),
    "idle_established": (int, False),
    "max_sessions": (int, False),
    "max_per_ip": (int, False),
    "new_per_sec": (float, False),
    "reap_interval": (float, False),
    "drop_zero_length": (bool, False),
}

SERVICE_KEYS = {
    "name": (str, True),
    "internal_port": (int, True),
    "srv_name": (str, True),
    "protocol": (str, False),
    "preset": (str, False),
    "priority": (int, False),
    "weight": (int, False),
    "ttl": (int, False),
    "record": (str, False),
}

AGENT_KEYS = {
    "interval": (int, False),
    "jitter": (int, False),
}

ALERT_KEYS = {
    "webhook": (str, False),
    "command": (str, False),
    "after_failures": (int, False),
}

FYRWIRE_KEYS = {
    "version": (int, False),
}

SECTIONS = {"fyrwire", "source", "provider", "node", "service", "agent", "alerts"}

ENUMS = {
    "service.protocol": {"tcp", "udp"},
    "service.record": {"srv", "https", "none"},
}


# Typo catcher: if you write 'relayy' or 'zoneid', complain loudly instead of silently ignoring it.
def _suggest(name: str, known) -> str:
    close = difflib.get_close_matches(name, sorted(known), n=1, cutoff=0.6)
    if close:
        return f" — did you mean {close[0]!r}?"
    return f" — valid keys: {', '.join(sorted(known))}"


def _check_type(where: str, key: str, value, want) -> None:
    if want is bool:
        if not isinstance(value, bool):
            raise FyrWireError(f"{where}.{key} must be true or false, got {value!r}")
        return
    if isinstance(value, bool) and want in (int, float, str):
        raise FyrWireError(f"{where}.{key} must be a {want.__name__}, got a boolean")
    if want is float and isinstance(value, int):
        return
    if not isinstance(value, want):
        raise FyrWireError(
            f"{where}.{key} must be a {want.__name__}, got "
            f"{type(value).__name__} ({value!r})")


def _check_table(where: str, table: dict, spec: dict) -> None:
    for key, value in table.items():
        if key not in spec:
            raise FyrWireError(f"unknown key {where}.{key}{_suggest(key, spec)}")
        _check_type(where, key, value, spec[key][0])
        base = where.split(".")[0].split("[")[0]
        allowed = ENUMS.get(f"{base}.{key}")
        if allowed and value not in allowed:
            raise FyrWireError(
                f"{where}.{key} must be one of {', '.join(sorted(allowed))}, "
                f"got {value!r}")
    for key, (_, required) in spec.items():
        if required and key not in table:
            raise FyrWireError(f"{where} is missing the required key {key!r}")


_CHECKABLE = {int, float, bool, str, dict, list}


def _resolve_annotation(ann):
    if ann is inspect.Parameter.empty:
        return None
    if isinstance(ann, str):
        text = ann.replace(" ", "")
        base = text.split("|")[0].split("[")[0]
        base = base.removeprefix("Optional[").removeprefix("typing.")
        return {"int": int, "float": float, "bool": bool, "str": str,
                "dict": dict, "list": list}.get(base)
    origin = typing.get_origin(ann)
    if origin is types.UnionType or origin is typing.Union:
        for arg in typing.get_args(ann):
            if arg is type(None):
                continue
            got = _resolve_annotation(arg)
            if got is not None:
                return got
        return None
    if origin is not None:
        return origin if origin in _CHECKABLE else None
    return ann if ann in _CHECKABLE else None


def _class_keys(cls) -> dict:
    out = {}
    for name, p in inspect.signature(cls.__init__).parameters.items():
        if name == "self" or p.kind is p.VAR_POSITIONAL:
            continue
        if p.kind is p.VAR_KEYWORD:
            out["**"] = (None, False)
            continue
        out[name] = (_resolve_annotation(p.annotation),
                     p.default is inspect.Parameter.empty)
    return out


def _check_typed_section(where: str, table: dict, registry: dict, kind: str) -> None:
    if not isinstance(table, dict):
        raise FyrWireError(
            f"[{where}] must be a table written as [{where}], not "
            f"{type(table).__name__}")
    if "type" not in table:
        raise FyrWireError(
            f"[{where}] needs a `type` key; available: "
            f"{', '.join(sorted(registry))}")
    tname = table["type"]
    if not isinstance(tname, str):
        raise FyrWireError(
            f"{where}.type must be a string naming a {kind}; available: "
            f"{', '.join(sorted(registry))}")
    cls = registry.get(tname)
    if cls is None:
        raise FyrWireError(
            f"unknown {kind} type {tname!r}"
            f"{_suggest(tname, registry)}")
    spec = _class_keys(cls)
    open_ended = spec.pop("**", None) is not None
    rest = {k: v for k, v in table.items() if k != "type"}
    for key, value in rest.items():
        if key not in spec:
            if open_ended:
                continue
            raise FyrWireError(
                f"unknown key {where}.{key} for {kind} type "
                f"{tname!r}{_suggest(key, spec)}")
        want = spec[key][0]
        if isinstance(value, str) and value.startswith("env:"):
            continue
        if want is not None:
            _check_type(where, key, value, want)
    for key, (_, required) in spec.items():
        if required and key not in rest:
            raise FyrWireError(
                f"[{where}] type {tname!r} requires {key!r}")


def migrate(cfg: dict) -> dict:
    return cfg


def validate(cfg: dict) -> dict:
    from . import providers, sources

    if not isinstance(cfg, dict):
        raise FyrWireError("the config must be a table of sections")
    for key, value in cfg.items():
        if key not in SECTIONS:
            raise FyrWireError(f"unknown section [{key}]{_suggest(key, SECTIONS)}")
        if key == "service":
            if not isinstance(value, list):
                raise FyrWireError(
                    "services must be written as [[service]] (double "
                    "brackets), one table per service")
        elif not isinstance(value, dict):
            raise FyrWireError(
                f"[{key}] must be a table; got {type(value).__name__}. "
                f"Write it as [{key}] with keys underneath, not "
                f"{key} = ...")

    meta = cfg.get("fyrwire") or {}
    _check_table("fyrwire", meta, FYRWIRE_KEYS)
    found = int(meta.get("version", SCHEMA_VERSION))
    if found > SCHEMA_VERSION:
        raise FyrWireError(
            f"this config is version {found}, but this FyrWire understands up "
            f"to {SCHEMA_VERSION}. Upgrade FyrWire, or remove the newer keys — "
            f"guessing what they mean would be worse than stopping.")
    if found < 1:
        raise FyrWireError(f"config version must be 1 or greater, got {found}")
    cfg = migrate(cfg)

    for key in ("source", "provider", "node"):
        if key not in cfg:
            raise FyrWireError(f"config is missing the [{key}] section")
    if not cfg.get("service"):
        raise FyrWireError("config defines no [[service]] entries")

    _check_typed_section("source", cfg["source"], sources._REGISTRY, "source")
    _check_typed_section("provider", cfg["provider"], providers._REGISTRY,
                         "provider")

    node = dict(cfg["node"])
    udp = node.pop("udp", None)
    _check_table("node", node, {k: v for k, v in NODE_KEYS.items() if k != "udp"})
    if udp is not None:
        if not isinstance(udp, dict):
            raise FyrWireError("[node.udp] must be a table")
        _check_table("node.udp", udp, UDP_KEYS)

    if not isinstance(cfg["service"], list):
        raise FyrWireError("services must be written as [[service]] (double "
                           "brackets), one table per service")
    for i, svc in enumerate(cfg["service"]):
        _check_table(f"service[{i}]", svc, SERVICE_KEYS)

    if "agent" in cfg:
        _check_table("agent", cfg["agent"], AGENT_KEYS)
    if "alerts" in cfg:
        _check_table("alerts", cfg["alerts"], ALERT_KEYS)

    if node.get("relay_udp") and not node.get("relay"):
        raise FyrWireError(
            "node.relay_udp is set but node.relay is not — UDP relaying is part "
            "of the relay, so this would silently do nothing")
    if node.get("udp") is not None and not node.get("relay_udp"):
        pass

    return cfg


def recheck_resolved(cfg: dict) -> None:
    from . import providers, sources
    for where, registry in (("source", sources._REGISTRY),
                            ("provider", providers._REGISTRY)):
        table = cfg.get(where) or {}
        cls = registry.get(table.get("type"))
        if cls is None:
            continue
        spec = _class_keys(cls)
        spec.pop("**", None)
        for key, value in table.items():
            if key == "type" or key not in spec:
                continue
            want = spec[key][0]
            if want is None or want is str:
                continue
            if isinstance(value, str):
                try:
                    if want is int:
                        int(value)
                    elif want is float:
                        float(value)
                    elif want is bool and value.lower() not in (
                            "true", "false", "1", "0", "yes", "no"):
                        raise ValueError(value)
                except ValueError:
                    raise FyrWireError(
                        f"{where}.{key} resolved to {value!r}, which is not a "
                        f"{want.__name__}") from None
