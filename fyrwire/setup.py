from __future__ import annotations

import os
import shutil

from . import detect
from .tui import BAD, DIM, LABEL, OK, RESET, TEXT, WARN


def _say(msg: str = "") -> None:
    print(msg)


def _ask(prompt: str, default: str = "") -> str:
    suffix = f" {DIM}[{default}]{RESET}" if default else ""
    try:
        got = input(f"{LABEL}?{RESET} {prompt}{suffix} ").strip()
    except (EOFError, KeyboardInterrupt):
        raise SystemExit("\ncancelled")
    return got or default


def _yes(prompt: str, default: bool = True) -> bool:
    d = "Y/n" if default else "y/N"
    got = _ask(f"{prompt} {DIM}({d}){RESET}").lower()
    return default if not got else got.startswith("y")


def _ok(msg: str) -> None:
    _say(f"  {OK}✓{RESET} {msg}")


def _bad(msg: str) -> None:
    _say(f"  {BAD}✗{RESET} {msg}")


def _warn(msg: str) -> None:
    _say(f"  {WARN}!{RESET} {msg}")


def run(config_path: str, force: bool = False) -> int:
    from . import banner

    _say(banner.render())
    _say(banner.tagline())
    _say()

    if os.path.exists(config_path) and not force:
        if not _yes(f"{config_path} exists. Overwrite?", False):
            return 1

    _say(f"{TEXT}Looking for a gateway that grants port forwards…{RESET}")
    cands = detect.gateway_candidates()
    gateway = None
    for gw, why in cands:
        pub = detect.natpmp_probe(gw)
        if pub:
            _ok(f"{gw} answers NAT-PMP — public address {pub}   {DIM}({why}){RESET}")
            gateway = gw
            break
        _say(f"  {DIM}· {gw} no reply ({why}){RESET}")

    source_type = "natpmp"
    if not gateway:
        _bad("nothing here speaks NAT-PMP.")
        _say(f"    {DIM}Common causes: the VPN is down, port forwarding is off in")
        _say(f"    your provider's settings, or your router uses UPnP instead.{RESET}")
        if _yes("Use the `command` source instead (any script that prints a port)?", False):
            source_type = "command"
        else:
            gateway = _ask("Gateway address to use anyway", "10.2.0.1")

    _say()
    _say(f"{TEXT}Cloudflare{RESET}")
    token = os.environ.get("FYRWIRE_CF_TOKEN", "")
    if token:
        _say(f"  {DIM}using FYRWIRE_CF_TOKEN from the environment{RESET}")
    else:
        _say(f"  {DIM}dash.cloudflare.com -> My Profile -> API Tokens ->")
        _say(f"  'Edit zone DNS' template. That is the only thing you need.{RESET}")
        token = _ask("API token")
    if not token:
        _bad("a token is required")
        return 1

    good, why = detect.verify_token(token)
    if not good:
        _bad(f"token rejected: {why}")
        return 1
    _ok(f"token valid ({why})")

    zones = detect.list_zones(token)
    if not zones:
        _bad("that token cannot see any zones")
        _say(f"    {DIM}Re-create it with Zone:DNS:Edit on the zone you want.{RESET}")
        return 1

    if len(zones) == 1:
        zone_id, zone_name = zones[0]
        _ok(f"one zone available: {zone_name}")
    else:
        _say(f"  {DIM}zones this token can edit:{RESET}")
        for i, (_, name) in enumerate(zones, 1):
            _say(f"    {DIM}{i}){RESET} {name}")
        pick = _ask("Which one", "1")
        idx = int(pick) - 1 if pick.isdigit() and 0 < int(pick) <= len(zones) else 0
        zone_id, zone_name = zones[idx]
        _ok(f"using {zone_name}")

    can, why = detect.can_edit_zone(token, zone_id)
    if not can:
        _bad(f"token cannot manage DNS in {zone_name}: {why}")
        _say(f"    {DIM}This is the failure that looks like success -- Cloudflare")
        _say(f"    answers HTTP 200 for it. Re-create with Zone:DNS:Edit.{RESET}")
        return 1
    _ok(f"token can manage DNS in {zone_name}")

    from . import presets as _presets

    _say()
    _say(f"{TEXT}What are you publishing?{RESET}")
    plist = _presets.listing()
    for i, pre in enumerate(plist, 1):
        colour = {"srv": OK, "srv-setup": WARN, "https": WARN,
                  "manual": DIM}.get(pre.follows, DIM)
        port = str(pre.port) if pre.port else "?"
        _say(f"  {DIM}{i:>2}){RESET} {pre.label:<26}{DIM}{port:>6} {pre.protocol}"
             f"{RESET}  {colour}{pre.follows}{RESET}")
    _say(f"  {DIM}     'srv' follows the port automatically; 'manual' means you")
    _say(f"  {DIM}     hand out host:port. `fyrwire presets` explains each.{RESET}")

    listening = detect.listening_ports()
    if listening:
        _say(f"  {DIM}listening here: "
             f"{', '.join(str(p) + (f' ({q})' if q else '') for p, q in listening)}{RESET}")

    services = []
    while True:
        pick = _ask("Number (blank when done)" if services else "Number")
        if not pick:
            if services:
                break
            continue
        if not (pick.isdigit() and 1 <= int(pick) <= len(plist)):
            _bad("pick a number from the list")
            continue
        pre = plist[int(pick) - 1]

        default_port = pre.port or (listening[0][0] if listening else 25565)
        port = _ask(f"  port for {pre.label}", str(default_port))
        key = _ask("  short name", pre.key)
        if any(x["name"] == key for x in services):
            _bad(f"already have a service called {key!r}")
            continue
        srv = pre.srv_name(zone_name) if pre.record == "srv" else \
            _ask("  hostname", f"{key}.{zone_name}")
        services.append({"name": key, "internal_port": int(port),
                         "protocol": pre.protocol, "srv_name": srv,
                         "record": pre.record, "preset": pre.key})
        _ok(f"{key}: {pre.protocol}/{port} -> {srv}")
        if pre.follows == "manual":
            _warn(f"  {pre.label} clients do not follow DNS ports — you will "
                  f"hand out host:port")
        elif pre.follows == "srv-setup":
            _warn(f"  {pre.label} reads the SRV record ONCE when the account is "
                  f"added, then caches it — a rotation breaks saved accounts")
        if pre.note:
            _say(f"    {DIM}{pre.note}{RESET}")
        if not _yes("Add another?", False):
            break

    a_name = _ask("Name for THIS MACHINE's address record", f"node.{zone_name}")

    src_block = (f'type     = "natpmp"\ngateway  = "{gateway}"\nlifetime = 120'
                 if source_type == "natpmp" else
                 'type    = "command"\nhost    = "203.0.113.1"   # or host_command\n'
                 'command = "echo 25565"   # must print host:port or a port')

    body = [f"# FyrWire — generated by `fyrwire setup`\n",
            "# The token is read from $FYRWIRE_CF_TOKEN, so this file is safe to commit.\n\n",
            f"[source]\n{src_block}\n\n",
            '[provider]\ntype      = "cloudflare"\n'
            'api_token = "env:FYRWIRE_CF_TOKEN"\n'
            f'zone_id   = "{zone_id}"\n\n',
            f'[node]\na_name = "{a_name}"\nttl    = 60\n\n']
    for s in services:
        body.append(f'[[service]]\nname          = "{s["name"]}"\n'
                    f'preset        = "{s["preset"]}"\n'
                    f'internal_port = {s["internal_port"]}\n'
                    f'protocol      = "{s["protocol"]}"\n'
                    f'record        = "{s["record"]}"\n'
                    f'srv_name      = "{s["srv_name"]}"\n\n')
    body.append("[agent]\ninterval = 45\n")

    os.makedirs(os.path.dirname(config_path) or ".", exist_ok=True)
    with open(config_path, "w") as fh:
        fh.write("".join(body))
    _say()
    _ok(f"wrote {config_path}")

    if not os.environ.get("FYRWIRE_CF_TOKEN"):
        _warn("export FYRWIRE_CF_TOKEN before running the agent:")
        _say(f'    {DIM}export FYRWIRE_CF_TOKEN="…"{RESET}')

    _say()
    _say(f"{TEXT}Checking it works (publishes nothing)…{RESET}")
    from .cli import build_plan, load_config
    from . import sources
    try:
        cfg = load_config(config_path, resolve_secrets=False)
        s = dict(cfg["source"])
        src = sources.build(s.pop("type"), s)
        plan = build_plan(cfg)
        res = src.resolve(plan.requests())
        _ok(f"address {res.host}")
        for svc in plan.services:
            port = res.ports.get(svc.key)
            if port:
                _ok(f"{svc.key}: {svc.internal_port} → {port}   {DIM}{svc.srv_name}{RESET}")
            else:
                _warn(f"{svc.key}: no mapping yet")
    except Exception as e:
        _warn(f"could not resolve yet: {e}")

    _say()
    _say(f"{TEXT}Next{RESET}")
    _say(f"  {LABEL}fyrwire once{RESET}      {DIM}publish for real, once{RESET}")
    _say(f"  {LABEL}fyrwire install{RESET}   {DIM}keep it running in the background{RESET}")
    _say(f"  {LABEL}fyrwire ui{RESET}        {DIM}dashboard{RESET}")
    return 0


def add_service(config_path: str) -> int:
    from .cli import build_plan, load_config

    if not os.path.exists(config_path):
        _bad(f"no config at {config_path} — run `fyrwire setup` first")
        return 1
    cfg = load_config(config_path, resolve_secrets=False)
    plan = build_plan(cfg)
    existing = {s.key for s in plan.services}

    _say(f"{TEXT}Existing services{RESET}")
    for s in plan.services:
        _say(f"  {DIM}{s.key:<14} {s.protocol}/{s.internal_port:<7} {s.srv_name}{RESET}")
    _say()

    from . import presets as _presets

    plist = _presets.listing()
    _say(f"{TEXT}What kind of service?{RESET}")
    for i, pre_ in enumerate(plist, 1):
        colour = {"srv": OK, "srv-setup": WARN, "https": WARN,
                  "manual": DIM}.get(pre_.follows, DIM)
        port_s = str(pre_.port) if pre_.port else "?"
        _say(f"  {DIM}{i:>2}){RESET} {pre_.label:<26}{DIM}{port_s:>6}{RESET}  "
             f"{colour}{pre_.follows}{RESET}")
    sel = _ask("Number", "9")
    pre = plist[int(sel) - 1] if sel.isdigit() and 1 <= int(sel) <= len(plist) \
        else _presets.get("generic-tcp")

    taken = {s.internal_port for s in plan.services}
    listening = [(p, proc) for p, proc in detect.listening_ports() if p not in taken]
    if listening:
        _say(f"  {DIM}listening and unpublished: "
             f"{', '.join(str(p) + (f' ({q})' if q else '') for p, q in listening)}{RESET}")
    default_port = pre.port or (listening[0][0] if listening else 0)
    port_in = _ask("Port", str(default_port) if default_port else "")
    if not port_in.isdigit():
        _bad("need a port number")
        return 1
    port = int(port_in)
    proc = next((q for p, q in listening if p == port), "")

    zone = plan.a_name.split(".", 1)[1] if "." in plan.a_name else plan.a_name
    key = _ask("Short name", pre.key if pre.port else (proc or f"svc{port}"))
    if key in existing:
        _bad(f"a service called {key!r} already exists")
        return 1
    proto = pre.protocol
    record = pre.record
    srv = pre.srv_name(zone) if record == "srv" else _ask("Hostname", f"{key}.{zone}")

    if pre.follows == "manual":
        _warn(f"{pre.label} clients do not follow DNS ports — hand out host:port")
    elif pre.follows == "srv-setup":
        _warn(f"{pre.label} reads the SRV record only at account setup, then "
              f"caches it — a rotation breaks saved accounts")
    if pre.note:
        _say(f"  {DIM}{pre.note}{RESET}")

    with open(config_path, "a") as fh:
        fh.write(f'\n[[service]]\nname          = "{key}"\n'
                 f'preset        = "{pre.key}"\n'
                 f'internal_port = {port}\n'
                 f'protocol      = "{proto}"\n'
                 f'record        = "{record}"\n'
                 f'srv_name      = "{srv}"\n')
    _ok(f"added {key} to {config_path}")

    try:
        build_plan(load_config(config_path, resolve_secrets=False))
        _ok("config still parses")
    except Exception as e:
        _bad(f"config no longer parses: {e}")
        return 1
    if proto.lower() != "tcp":
        _warn("UDP services get an SRV record but are not relayed yet")
    _say(f"\n  {LABEL}fyrwire once{RESET}   {DIM}publish it{RESET}")
    return 0


def remove_service(config_path: str, name: str, keep_dns: bool = False,
                   state_path: str | None = None, yes: bool = False) -> int:
    from .cli import build_agent, build_plan, load_config

    if not os.path.exists(config_path):
        _bad(f"no config at {config_path}")
        return 1
    cfg = load_config(config_path, resolve_secrets=False)
    plan = build_plan(cfg)
    try:
        svc = plan.service(name)
    except KeyError:
        _bad(f"no service called {name!r}")
        _say(f"  {DIM}have: {', '.join(s.key for s in plan.services)}{RESET}")
        return 1

    _say(f"  {DIM}{svc.key}: {svc.protocol}/{svc.internal_port} -> {svc.srv_name}{RESET}")
    if not keep_dns:
        _say(f"  {WARN}!{RESET} this will DELETE the DNS record {svc.srv_name}")
    if not yes and not _yes("Remove it?", False):
        return 1

    if not keep_dns:
        try:
            cfg_full = load_config(config_path)
            agent = build_agent(cfg_full, state_path or "/dev/null",
                                log=lambda *_: None)
            what = agent.provider.delete("SRV", svc.srv_name)
            _ok(what or f"{svc.srv_name} was already absent")
        except Exception as e:
            _bad(f"could not delete the DNS record: {e}")
            _say(f"    {DIM}config left untouched. Fix the above, or re-run with"
                 f" --keep-dns to remove it locally only.{RESET}")
            return 1

    with open(config_path) as fh:
        text = fh.read()
    blocks, current = [], []
    for line in text.splitlines(keepends=True):
        if line.strip() == "[[service]]":
            blocks.append(current)
            current = [line]
        elif line.strip().startswith("[") and current and current[0].strip() == "[[service]]":
            blocks.append(current)
            current = [line]
        else:
            current.append(line)
    blocks.append(current)
    kept = [b for b in blocks
            if not (b and b[0].strip() == "[[service]]"
                    and any(f'"{svc.key}"' in l and "name" in l for l in b))]
    with open(config_path, "w") as fh:
        fh.write("".join("".join(b) for b in kept))

    try:
        left = build_plan(load_config(config_path, resolve_secrets=False))
    except Exception as e:
        _bad(f"config no longer parses after removal: {e}")
        return 1
    if svc.key in {s.key for s in left.services}:
        _bad("service still present after rewrite — config not in the expected shape")
        return 1
    _ok(f"removed {svc.key} from {config_path}")

    if state_path and os.path.exists(state_path):
        from .agent import AgentState
        st = AgentState.load(state_path)
        if st.last_ports.pop(svc.key, None) is not None:
            st.save(state_path)
            _ok("cleared its stored port")

    _say(f"\n  {DIM}the relay for it stops on the next reconcile{RESET}")
    return 0


UNIT = """[Unit]
Description=FyrWire — keep names pointed at a moving endpoint
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={exe} --config {config} run
Restart=always
RestartSec=10
# The token must not live in the unit file: systemd unit files are
# world-readable by default.
EnvironmentFile=%h/.config/fyrwire/env

[Install]
WantedBy=default.target
"""


def install(config_path: str) -> int:
    if shutil.which("systemctl") is None:
        _bad("systemd not found.")
        _say(f"  {DIM}macOS: use launchd. Windows: Task Scheduler, or run "
             f"`fyrwire run` under nssm.{RESET}")
        return 1

    unit_dir = os.path.expanduser("~/.config/systemd/user")
    os.makedirs(unit_dir, exist_ok=True)
    exe = shutil.which("fyrwire") or "python3 -m fyrwire"
    path = os.path.join(unit_dir, "fyrwire.service")
    with open(path, "w") as fh:
        fh.write(UNIT.format(exe=exe, config=config_path))
    _ok(f"wrote {path}")

    env_path = os.path.expanduser("~/.config/fyrwire/env")
    if not os.path.exists(env_path):
        os.makedirs(os.path.dirname(env_path), exist_ok=True)
        with open(env_path, "w") as fh:
            fh.write("FYRWIRE_CF_TOKEN=\n")
        os.chmod(env_path, 0o600)
        _ok(f"wrote {env_path} (mode 600) — put your token in it")

    _say()
    _say(f"  {LABEL}systemctl --user daemon-reload{RESET}")
    _say(f"  {LABEL}systemctl --user enable --now fyrwire{RESET}")
    _say(f"  {LABEL}loginctl enable-linger $USER{RESET}   "
         f"{DIM}so it runs without you logged in{RESET}")
    return 0
