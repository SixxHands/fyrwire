from __future__ import annotations

import os
import re
import shutil

_ANSI = re.compile(r"\x1b\[[0-9;]*m")

DIM = "\033[38;2;120;130;140m"
TEXT = "\033[38;2;214;222;228m"
LABEL = "\033[38;2;255;138;60m"
OK = "\033[38;2;110;220;150m"
WARN = "\033[38;2;240;190;90m"
BAD = "\033[38;2;240;110;110m"
RESET = "\033[0m"


def vis(s: str) -> int:
    return len(_ANSI.sub("", s))


def clip(s: str, width: int) -> str:
    if vis(s) <= width:
        return s
    out, seen = [], 0
    i = 0
    while i < len(s):
        m = _ANSI.match(s, i)
        if m:
            out.append(m.group())
            i = m.end()
            continue
        if seen >= width - 1:
            out.append("…")
            break
        out.append(s[i])
        seen += 1
        i += 1
    return "".join(out) + RESET


def term_width(default: int = 96) -> int:
    try:
        return os.get_terminal_size(1).columns
    except OSError:
        pass
    try:
        return shutil.get_terminal_size((default, 24)).columns
    except Exception:
        return default


PLAIN = False


def panel(label: str, rows: list[str], status: str = "", width: int = 0,
          status_colour: str = OK) -> list[str]:
    if PLAIN:
        head = f"{label}" + (f"  [{status}]" if status else "")
        return [head] + [f"  {_ANSI.sub('', r)}" for r in rows]

    width = width or min(term_width(), 100)
    lab = f"┤ {label} ├"
    st = f"┤ {status} ├" if status else ""

    if width < len(lab) + len(st) + 10:
        st = ""

    if st:
        fill = max(0, width - 12 - len(label) - len(status))
        top = (f"{DIM}╭─┤ {LABEL}{label}{DIM} ├{'─' * fill}"
               f"┤ {status_colour}{status}{DIM} ├─╮{RESET}")
    else:
        fill = max(0, width - 8 - len(label))
        top = f"{DIM}╭─┤ {LABEL}{label}{DIM} ├{'─' * fill}─╮{RESET}"

    out = [top]
    for r in rows:
        r = clip(r, width - 4)
        pad = " " * max(0, width - 4 - vis(r))
        out.append(f"{DIM}│{RESET} {r}{pad} {DIM}│{RESET}")
    out.append(f"{DIM}╰{'─' * (width - 2)}╯{RESET}")
    return out


def kv(key: str, value: str, colour: str = TEXT, keyw: int = 12) -> str:
    return f"{DIM}{key:<{keyw}}{RESET}{colour}{value}{RESET}"


def cmd(invocation: str, description: str, w: int = 26) -> str:
    return f"{LABEL}{invocation:<{w}}{RESET}{DIM}{description}{RESET}"


def dashboard(plan, state, source_name: str, provider_name: str,
              last: dict | None = None, width: int = 0) -> str:
    from . import banner

    width = width or min(term_width(), 100)
    if PLAIN:
        out: list[str] = [f"FyrWire — {banner.TAGLINE}", ""]
    else:
        out = [banner.render(), banner.tagline(), ""]

    if state.consecutive_failures:
        node_status, colour = f"DEGRADED x{state.consecutive_failures}", BAD
    elif state.last_host:
        node_status, colour = "LIVE", OK
    else:
        node_status, colour = "NEVER PUBLISHED", WARN

    out += panel("fyrwire://node", [
        kv("name", plan.a_name),
        kv("address", state.last_host or "—", TEXT if state.last_host else DIM),
        kv("source", source_name),
        kv("provider", provider_name),
        kv("ttl", f"{plan.ttl}s"),
    ], node_status, width, colour)
    out.append("")

    skipped = set((last or {}).get("skipped") or [])
    EXT = 10
    lead = "stat" if PLAIN else " "
    rows = [f"{DIM}{lead} {'service':<10}{'proto':<7}{'internal':>9}   "
            f"{'external':>{EXT}}   srv{RESET}"]
    for svc in plan.services:
        ext = state.last_ports.get(svc.key)
        if svc.key in skipped:
            mark, ec, word = BAD, f"{BAD}{'unresolved':>{EXT}}{RESET}", "FAIL"
        elif ext:
            mark, ec, word = OK, f"{OK}{ext:>{EXT}}{RESET}", "ok  "
        else:
            mark, ec, word = DIM, f"{DIM}{'—':>{EXT}}{RESET}", "--  "
        bullet = word if PLAIN else f"{mark}●{RESET}"
        rows.append(
            f"{bullet} {TEXT}{svc.key:<10}{RESET}"
            f"{DIM}{svc.protocol:<7}{RESET}"
            f"{TEXT}{svc.internal_port:>9}{RESET}   {ec}   "
            f"{DIM}{svc.srv_name}{RESET}"
        )
    live = sum(1 for s in plan.services if state.last_ports.get(s.key))
    out += panel("fyrwire://services", rows,
                 f"{live}/{len(plan.services)}", width,
                 OK if live == len(plan.services) else WARN)
    out.append("")

    rows = [
        kv("publishes", str(state.total_publishes)),
        kv("failures", str(state.total_failures),
           BAD if state.total_failures else TEXT),
    ]
    if state.history:
        rows.append("")
        for h in state.history[:4]:
            when = h["at"].replace("T", " ").replace("+00:00", "Z")
            rows.append(f"{DIM}{when}{RESET}  {TEXT}{h.get('host','')}{RESET}")
            for c in h.get("changes", [])[:3]:
                rows.append(f"  {DIM}{c}{RESET}")
    else:
        rows.append(f"{DIM}no changes recorded yet{RESET}")
    out += panel("fyrwire://activity", rows, "", width)
    out.append("")

    out += panel("fyrwire://commands", [
        cmd("fyrwire ui --watch 5", "redraw this screen every 5s"),
        cmd("fyrwire once --dry-run", "resolve and print, publish nothing"),
        cmd("fyrwire once", "reconcile now"),
        cmd("fyrwire run", "reconcile forever"),
        cmd("fyrwire status --json", "machine-readable state"),
        cmd("fyrwire sources", "list available plug-ins"),
    ], "", width)

    if not PLAIN:
        out.append(f"{DIM}  --probe asks the gateway live · --plain for flat "
                   f"text · NO_COLOR respected{RESET}")

    return "\n".join(out)


def _selfcheck() -> None:
    for w in (60, 80, 100, 132):
        for status in ("", "LIVE", "DEGRADED x12"):
            lines = panel("fyrwire://node", ["a", "bb", "ccc"], status, w)
            widths = {vis(l) for l in lines}
            assert widths == {w}, f"width={w} status={status!r} -> {sorted(widths)}"
    print("panel geometry ok at 60/80/100/132 with and without a status token")


if __name__ == "__main__":
    _selfcheck()
