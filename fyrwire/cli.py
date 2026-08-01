from __future__ import annotations

import argparse
import json
import os
import sys

from . import banner, providers, sources
from .agent import Agent
from .models import FyrWireError

try:
    import tomllib
except ModuleNotFoundError:
    import tomli as tomllib

DEFAULT_CONFIG = os.environ.get(
    "FYRWIRE_CONFIG",
    os.path.join(os.environ.get("XDG_CONFIG_HOME", os.path.expanduser("~/.config")),
                 "fyrwire", "fyrwire.toml"),
)
DEFAULT_STATE = os.path.join(
    os.environ.get("XDG_STATE_HOME", os.path.expanduser("~/.local/state")),
    "fyrwire", "state.json",
)


def load_config(path: str, resolve_secrets: bool = True) -> dict:
    if not os.path.exists(path):
        raise FyrWireError(f"no config at {path} — run `fyrwire init` to write a starter")
    with open(path, "rb") as fh:
        cfg = tomllib.load(fh)
    for key in ("source", "provider", "record"):
        if key not in cfg:
            raise FyrWireError(f"config is missing the [{key}] section")
    if not resolve_secrets:
        return cfg
    prov = dict(cfg["provider"])
    for k, v in list(prov.items()):
        if isinstance(v, str) and v.startswith("env:"):
            env = v[4:]
            got = os.environ.get(env)
            if not got:
                raise FyrWireError(f"provider.{k} references ${env}, which is unset")
            prov[k] = got
    cfg["provider"] = prov
    return cfg


def build_agent(cfg: dict, state_path: str, log=print) -> Agent:
    s = dict(cfg["source"])
    p = dict(cfg["provider"])
    src = sources.build(s.pop("type"), s)
    prov = providers.build(p.pop("type"), p)
    return Agent(src, prov, cfg["record"], state_path, log=log)


STARTER = '''# FyrWire — keep a name pointed at a moving endpoint.
#
# Secrets: any value written as "env:NAME" is read from that environment
# variable instead, so this file stays safe to commit.

[source]
# natpmp | upnp | command | static
type          = "natpmp"
gateway       = "10.2.0.1"     # VPN gateway that grants the forward
internal_port = 25565          # the local port the service listens on
lifetime      = 120

[provider]
type      = "cloudflare"
api_token = "env:FYRWIRE_CF_TOKEN"
zone_id   = "env:FYRWIRE_CF_ZONE"

[record]
# A record carries the ADDRESS, SRV carries the PORT. Both move together.
a_name   = "node.example.com"
srv_name = "_minecraft._tcp.play.example.com"
ttl      = 60

[agent]
interval = 45
'''


def cmd_init(args) -> int:
    path = args.config
    if os.path.exists(path) and not args.force:
        print(f"{path} already exists (use --force to overwrite)")
        return 1
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    with open(path, "w") as fh:
        fh.write(STARTER)
    print(f"wrote {path}")
    print("set FYRWIRE_CF_TOKEN and FYRWIRE_CF_ZONE, then run: fyrwire once --dry-run")
    return 0


def cmd_sources(args) -> int:
    print("sources:   " + ", ".join(sources.available()))
    print("providers: " + ", ".join(providers.available()))
    return 0


def cmd_once(args) -> int:
    cfg = load_config(args.config, resolve_secrets=not args.dry_run)
    if args.dry_run:
        s = dict(cfg["source"])
        src = sources.build(s.pop("type"), s)
        ep = src.discover()
        rec = cfg["record"]
        print(f"discovered  {ep}")
        print(f"would set   A   {rec['a_name']} -> {ep.host}")
        print(f"would set   SRV {rec['srv_name']} -> 0 5 {ep.port} {rec['a_name']}")
        return 0
    agent = build_agent(cfg, args.state)
    report = agent.tick(force=args.force)
    print(json.dumps(report, indent=2) if args.json else _human(report))
    return 0 if report.get("ok") else 1


def cmd_run(args) -> int:
    cfg = load_config(args.config)
    agent = build_agent(cfg, args.state)
    interval = args.interval or int(cfg.get("agent", {}).get("interval", 45))
    if not args.quiet:
        print(banner.render())
        print(banner.tagline())
        print(f"  source={agent.source.name}  provider={agent.provider.name}  every {interval}s\n")
    try:
        agent.run(interval, max_cycles=args.cycles)
    except KeyboardInterrupt:
        print("stopped")
    return 0


def cmd_status(args) -> int:
    from .agent import AgentState
    st = AgentState.load(args.state)
    if args.json:
        print(st.to_json())
        return 0
    print(banner.render())
    print(banner.tagline() + "\n")
    print(f"  last endpoint    {st.last_endpoint or '—'}")
    print(f"  last published   {st.last_published or '—'}")
    print(f"  publishes        {st.total_publishes}")
    print(f"  failures         {st.total_failures} "
          f"({st.consecutive_failures} consecutive)")
    if st.history:
        print("\n  recent changes")
        for h in st.history[:5]:
            print(f"    {h['at']}  {h['endpoint']}")
    return 0


def _human(r: dict) -> str:
    if not r.get("ok"):
        return f"FAILED  {r.get('error')}\n        kept {r.get('kept') or 'nothing'} in DNS"
    if r.get("changes"):
        return "\n".join(["OK  " + r["endpoint"]] + ["    " + c for c in r["changes"]])
    return f"OK  {r['endpoint']}  (already correct)"


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(prog="fyrwire",
                                 description="Keep a name pointed at a moving endpoint.")
    ap.add_argument("--config", default=DEFAULT_CONFIG)
    ap.add_argument("--state", default=DEFAULT_STATE)
    sub = ap.add_subparsers(dest="cmd", required=True)

    p = sub.add_parser("init", help="write a starter config"); p.add_argument("--force", action="store_true"); p.set_defaults(fn=cmd_init)
    p = sub.add_parser("sources", help="list plug-ins"); p.set_defaults(fn=cmd_sources)
    p = sub.add_parser("once", help="one reconcile cycle")
    p.add_argument("--dry-run", action="store_true"); p.add_argument("--force", action="store_true")
    p.add_argument("--json", action="store_true"); p.set_defaults(fn=cmd_once)
    p = sub.add_parser("run", help="loop forever")
    p.add_argument("--interval", type=int); p.add_argument("--cycles", type=int)
    p.add_argument("--quiet", action="store_true"); p.set_defaults(fn=cmd_run)
    p = sub.add_parser("status", help="what the agent last did")
    p.add_argument("--json", action="store_true"); p.set_defaults(fn=cmd_status)

    args = ap.parse_args(argv)
    try:
        return args.fn(args)
    except FyrWireError as e:
        print(f"fyrwire: {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
