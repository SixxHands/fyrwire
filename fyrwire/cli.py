from __future__ import annotations

import argparse
import json
import os
import sys

from . import banner, providers, sources, tui, setup
from .agent import Agent
from .models import FyrWireError, NodePlanData, Service

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
    for key in ("source", "provider", "node"):
        if key not in cfg:
            raise FyrWireError(f"config is missing the [{key}] section")
    if not cfg.get("service"):
        raise FyrWireError("config defines no [[service]] entries")
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


def build_plan(cfg: dict) -> NodePlanData:
    node = cfg["node"]
    svcs = []
    seen = set()
    for i, s in enumerate(cfg["service"]):
        key = s.get("name") or f"svc{i}"
        if key in seen:
            raise FyrWireError(f"duplicate service name {key!r}")
        seen.add(key)
        for req in ("internal_port", "srv_name"):
            if req not in s:
                raise FyrWireError(f"service {key!r} is missing {req}")
        svcs.append(Service(
            key=key,
            internal_port=int(s["internal_port"]),
            srv_name=s["srv_name"],
            protocol=s.get("protocol", "tcp"),
            priority=int(s.get("priority", 0)),
            weight=int(s.get("weight", 5)),
            ttl=int(s["ttl"]) if "ttl" in s else None,
        ))
    return NodePlanData(a_name=node["a_name"], services=tuple(svcs),
                    ttl=int(node.get("ttl", 60)))


def build_agent(cfg: dict, state_path: str, log=print) -> Agent:
    s = dict(cfg["source"])
    p = dict(cfg["provider"])
    src = sources.build(s.pop("type"), s)
    prov = providers.build(p.pop("type"), p)
    return Agent(src, prov, build_plan(cfg), state_path, log=log)


STARTER = '''# FyrWire — keep names pointed at a moving endpoint.
#
# Secrets: any value written as "env:NAME" is read from that environment
# variable instead, so this file stays safe to commit.

[source]
# natpmp | upnp | command | static
type     = "natpmp"
gateway  = "10.2.0.1"          # VPN gateway that grants the forwards
lifetime = 120

[provider]
type      = "cloudflare"
api_token = "env:FYRWIRE_CF_TOKEN"
zone_id   = "env:FYRWIRE_CF_ZONE"

[node]
# ONE A record for the machine. Every service points its SRV at this name, so
# the address is published once and cannot disagree with itself.
a_name = "node.example.com"
ttl    = 60

# One [[service]] per thing you expose. Each gets its own forwarded port and
# its own SRV record; they share the A record above.
[[service]]
name          = "minecraft"
internal_port = 25565
srv_name      = "_minecraft._tcp.play.example.com"

[[service]]
name          = "valheim"
internal_port = 2456
protocol      = "udp"
srv_name      = "_valheim._udp.vh.example.com"

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


def cmd_setup(args) -> int:
    return setup.run(args.config, force=args.force)


def cmd_install(args) -> int:
    return setup.install(args.config)


def cmd_doctor(args) -> int:
    from . import detect
    cfg = load_config(args.config, resolve_secrets=False)
    plan = build_plan(cfg)
    ok = True

    src = cfg["source"]
    if src.get("type") == "natpmp":
        gw = src.get("gateway", "")
        pub = detect.natpmp_probe(gw)
        print(f"  gateway {gw:<16} " + (f"OK  public {pub}" if pub else "NO REPLY"))
        ok &= bool(pub)

    tok = os.environ.get("FYRWIRE_CF_TOKEN", "")
    zone = cfg["provider"].get("zone_id", "")
    if tok and zone:
        good, why = detect.verify_token(tok)
        print(f"  token                    " + ("OK" if good else f"BAD  {why}"))
        ok &= good
        if good:
            can, why = detect.can_edit_zone(tok, zone)
            print(f"  zone access              " + ("OK" if can else f"DENIED  {why}"))
            ok &= can
    else:
        print("  token                    SKIPPED (FYRWIRE_CF_TOKEN unset)")

    listening = {p for p, _ in detect.listening_ports()}
    for svc in plan.services:
        live = svc.internal_port in listening
        print(f"  service {svc.key:<16} " +
              (f"OK  :{svc.internal_port} listening" if live
               else f"WARN :{svc.internal_port} nothing listening"))
    return 0 if ok else 1


def cmd_sources(args) -> int:
    print("sources:   " + ", ".join(sources.available()))
    print("providers: " + ", ".join(providers.available()))
    return 0


def cmd_once(args) -> int:
    cfg = load_config(args.config, resolve_secrets=not args.dry_run)
    if args.dry_run:
        s = dict(cfg["source"])
        src = sources.build(s.pop("type"), s)
        plan = build_plan(cfg)
        res = src.resolve(plan.requests())
        print(f"address     {res.host}")
        print(f"would set   A   {plan.a_name} -> {res.host}")
        for svc in plan.services:
            port = res.ports.get(svc.key)
            if port is None:
                print(f"  [{svc.key}] UNRESOLVED — its record would be left alone")
                continue
            print(f"would set   SRV {svc.srv_name} -> "
                  f"{svc.priority} {svc.weight} {port} {plan.a_name}")
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
    print(f"  address          {st.last_host or '—'}")
    if st.last_ports:
        for k in sorted(st.last_ports):
            print(f"    {k:<14} :{st.last_ports[k]}")
    else:
        print("    (no services published yet)")
    print(f"  publishes        {st.total_publishes}")
    print(f"  failures         {st.total_failures} "
          f"({st.consecutive_failures} consecutive)")
    if st.history:
        print("\n  recent changes")
        for h in st.history[:5]:
            print(f"    {h['at']}  {h['endpoint']}")
    return 0


def cmd_ui(args) -> int:
    from .agent import AgentState

    tui.PLAIN = args.plain
    cfg = load_config(args.config, resolve_secrets=False)
    plan = build_plan(cfg)
    st = AgentState.load(args.state)
    src_name = cfg["source"].get("type", "?")
    prov_name = cfg["provider"].get("type", "?")

    def frame() -> str:
        last = None
        state = st
        if args.probe:
            s2 = dict(cfg["source"])
            src = sources.build(s2.pop("type"), s2)
            try:
                res = src.resolve(plan.requests())
                state = AgentState(**{**vars(st)})
                state.last_host = res.host
                state.last_ports = {**st.last_ports, **res.ports}
                last = {"skipped": sorted(res.partial)}
            except FyrWireError as e:
                last = {"skipped": [s.key for s in plan.services]}
                state = AgentState(**{**vars(st)})
                state.consecutive_failures = max(1, st.consecutive_failures)
                print(f"probe failed: {e}", file=sys.stderr)
        return tui.dashboard(plan, state, src_name, prov_name, last, args.width)

    if args.plain:
        tui.PLAIN = True

    if not args.watch:
        print(frame())
        return 0

    import time
    try:
        while True:
            sys.stdout.write("\033[2J\033[H" + frame() + "\n")
            sys.stdout.flush()
            time.sleep(args.watch)
    except KeyboardInterrupt:
        sys.stdout.write("\033[?25h\n")
    return 0


def _human(r: dict) -> str:
    if not r.get("ok"):
        return f"FAILED  {r.get('error')}\n        kept {r.get('kept') or 'nothing'} in DNS"
    if r.get("changes"):
        return "\n".join(["OK  " + r["endpoint"]] + ["    " + c for c in r["changes"]])
    return f"OK  {r['endpoint']}  (already correct)"


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(prog="fyrwire",
                                 description="Keep your connections... connected!")
    ap.add_argument("--config", default=DEFAULT_CONFIG)
    ap.add_argument("--state", default=DEFAULT_STATE)
    sub = ap.add_subparsers(dest="cmd", required=True)

    p = sub.add_parser("init", help="write a starter config"); p.add_argument("--force", action="store_true"); p.set_defaults(fn=cmd_init)
    p = sub.add_parser("setup", help="guided first-run setup")
    p.add_argument("--force", action="store_true"); p.set_defaults(fn=cmd_setup)
    p = sub.add_parser("install", help="run it in the background (systemd user unit)")
    p.set_defaults(fn=cmd_install)
    p = sub.add_parser("doctor", help="check an existing setup, publish nothing")
    p.set_defaults(fn=cmd_doctor)
    p = sub.add_parser("sources", help="list plug-ins"); p.set_defaults(fn=cmd_sources)
    p = sub.add_parser("once", help="one reconcile cycle")
    p.add_argument("--dry-run", action="store_true"); p.add_argument("--force", action="store_true")
    p.add_argument("--json", action="store_true"); p.set_defaults(fn=cmd_once)
    p = sub.add_parser("run", help="loop forever")
    p.add_argument("--interval", type=int); p.add_argument("--cycles", type=int)
    p.add_argument("--quiet", action="store_true"); p.set_defaults(fn=cmd_run)
    p = sub.add_parser("ui", help="dashboard")
    p.add_argument("--probe", action="store_true",
                   help="ask the gateway for current ports (no DNS credentials needed)")
    p.add_argument("--watch", type=float, metavar="SECS", help="redraw every SECS")
    p.add_argument("--width", type=int, default=0)
    p.add_argument("--plain", action="store_true",
                   help="flat text: no borders, no colour (screen readers, pipes)")
    p.set_defaults(fn=cmd_ui)
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
