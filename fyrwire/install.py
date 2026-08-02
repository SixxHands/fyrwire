from __future__ import annotations

import os
import platform
import shutil
import subprocess
import sys

from . import paths
from .models import FyrWireError

LABEL = "com.fyrwire.agent"
UNIT = "fyrwire.service"
TASK = "FyrWire"


def _launcher(config_path: str) -> list[str]:
    exe = sys.executable
    if platform.system() == "Windows":
        cand = exe.replace("python.exe", "pythonw.exe")
        if cand != exe and os.path.exists(cand):
            exe = cand
    return [exe, "-m", "fyrwire", "run", "--config", config_path]


def _quote_win(parts: list[str]) -> str:
    return " ".join(f'"{p}"' if " " in p else p for p in parts)


def _systemd_arg(a: str) -> str:
    a = a.replace("%", "%%")
    if any(c in a for c in ' "\\\t'):
        return '"' + a.replace("\\", "\\\\").replace('"', '\\"') + '"'
    return a


def systemd_unit(config_path: str, token_env: str = "FYRWIRE_CF_TOKEN") -> str:
    exec_start = " ".join(_systemd_arg(a) for a in _launcher(config_path))
    return f"""[Unit]
Description=FyrWire — keep names pointed at a moving endpoint
# Not After=network-online.target: the VPN comes up long after the network
# does, and a hard dependency would just delay the first attempt. The agent
# retries on its own interval, so starting early and failing once is cheaper
# than waiting for a target that may never fire.
After=network.target

[Service]
Type=simple
ExecStart={exec_start}
Restart=always
RestartSec=15
# The token is NOT written here. A unit file is world-readable, and this file
# would then be the one place a secret leaks from.
Environment={token_env}=
# Read it from a mode-600 file instead:
#   systemctl --user edit {UNIT}
#   [Service]
#   EnvironmentFile=%h/.config/fyrwire/env
EnvironmentFile=-%h/.config/fyrwire/env

[Install]
WantedBy=default.target
"""


def install_systemd(config_path: str, log=print) -> int:
    d = paths.service_dir()
    os.makedirs(d, exist_ok=True)
    unit_path = os.path.join(d, UNIT)
    with open(unit_path, "w") as fh:
        fh.write(systemd_unit(config_path))
    log(f"wrote {unit_path}")
    if not shutil.which("systemctl"):
        log("systemctl not found — the unit is written, but you will need to "
            "start it however your init system does that")
        return 0
    subprocess.run(["systemctl", "--user", "daemon-reload"], check=False)
    log("next:")
    log(f"  systemctl --user enable --now {UNIT}")
    log("")
    log("  and, if this machine is headless or you log out:")
    log(f"    sudo loginctl enable-linger {os.environ.get('USER', 'you')}")
    log("  Without lingering, a user service stops when your last session ends —")
    log("  so DNS would only update while you happen to be logged in.")
    return 0


def launchd_plist(config_path: str) -> str:
    from xml.sax.saxutils import escape
    args = "".join(f"        <string>{escape(a)}</string>\n"
                   for a in _launcher(config_path))
    logdir = escape(paths.state_dir())
    return f"""<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{LABEL}</string>
    <key>ProgramArguments</key>
    <array>
{args}    </array>
    <key>RunAtLoad</key>
    <true/>
    <!-- KeepAlive restarts the agent if it exits for any reason, which is what
         Restart=always does on systemd. SuccessfulExit=false means a clean
         exit 0 is respected as deliberate rather than fought. -->
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>ThrottleInterval</key>
    <integer>15</integer>
    <key>StandardOutPath</key>
    <string>{logdir}/fyrwire.log</string>
    <key>StandardErrorPath</key>
    <string>{logdir}/fyrwire.log</string>
    <!-- A LaunchAgent inherits almost no environment, so the token has to be
         named here or read from the config's env file. Prefer the latter:
         this plist is readable by anything running as you. -->
</dict>
</plist>
"""


def install_launchd(config_path: str, log=print) -> int:
    d = paths.service_dir()
    os.makedirs(d, exist_ok=True)
    os.makedirs(paths.state_dir(), exist_ok=True)
    plist = os.path.join(d, f"{LABEL}.plist")
    with open(plist, "w") as fh:
        fh.write(launchd_plist(config_path))
    log(f"wrote {plist}")
    if shutil.which("launchctl"):
        uid = os.getuid()
        r = subprocess.run(["launchctl", "bootstrap", f"gui/{uid}", plist],
                           capture_output=True, text=True)
        if r.returncode != 0:
            subprocess.run(["launchctl", "load", "-w", plist], check=False)
        log(f"loaded {LABEL}")
        log("next:")
        log(f"  launchctl list | grep {LABEL}        # confirm it is running")
        log(f"  launchctl bootout gui/{uid}/{LABEL}   # to stop it")
    else:
        log("launchctl not found — the plist is written but not loaded")
    log("")
    log("UNVERIFIED: this installer has never been run on macOS. Report what "
        "breaks rather than assuming it works.")
    return 0


def schtasks_command(config_path: str) -> list[str]:
    return ["schtasks", "/Create", "/TN", TASK, "/SC", "ONLOGON",
            "/TR", _quote_win(_launcher(config_path)), "/F"]


def install_windows(config_path: str, log=print) -> int:
    if not shutil.which("schtasks"):
        raise FyrWireError("schtasks not found; cannot register a startup task")
    cmd = schtasks_command(config_path)
    r = subprocess.run(cmd, capture_output=True, text=True)
    if r.returncode != 0:
        raise FyrWireError(
            f"schtasks failed: {(r.stderr or r.stdout).strip()[:200]}")
    log(f"registered scheduled task {TASK!r} (runs at logon, as you)")
    log("next:")
    log(f"  schtasks /Run   /TN {TASK}      # start it now")
    log(f"  schtasks /Query /TN {TASK}      # check it")
    log(f"  schtasks /Delete /TN {TASK} /F  # remove it")
    log("")
    log("Set your DNS token as a user environment variable so the task can "
        "see it:")
    log("  setx FYRWIRE_CF_TOKEN \"...\"")
    log("")
    log("A logon task runs only after you log in. For a machine that should "
        "serve while signed out, use /SC ONSTART with /RU SYSTEM — but that "
        "needs Administrator and changes which profile the config is read "
        "from, so FyrWire does not do it for you.")
    log("")
    log("UNVERIFIED: this installer has never been run on Windows.")
    return 0


def install(config_path: str, log=print) -> int:
    system = platform.system()
    if system == "Linux":
        return install_systemd(config_path, log)
    if system == "Darwin":
        return install_launchd(config_path, log)
    if system == "Windows":
        return install_windows(config_path, log)
    raise FyrWireError(
        f"no service installer for {system or 'this platform'}. FyrWire itself "
        f"still runs — start `fyrwire run` however this system starts things.")


def uninstall(config_path: str, log=print) -> int:
    system = platform.system()
    if system == "Linux":
        unit = os.path.join(paths.service_dir(), UNIT)
        if shutil.which("systemctl"):
            subprocess.run(["systemctl", "--user", "disable", "--now", UNIT],
                           capture_output=True)
        if os.path.exists(unit):
            os.remove(unit)
            log(f"removed {unit}")
        return 0
    if system == "Darwin":
        plist = os.path.join(paths.service_dir(), f"{LABEL}.plist")
        if shutil.which("launchctl"):
            subprocess.run(["launchctl", "bootout", f"gui/{os.getuid()}/{LABEL}"],
                           capture_output=True)
        if os.path.exists(plist):
            os.remove(plist)
            log(f"removed {plist}")
        return 0
    if system == "Windows":
        subprocess.run(["schtasks", "/Delete", "/TN", TASK, "/F"],
                       capture_output=True)
        log(f"removed scheduled task {TASK!r}")
        return 0
    raise FyrWireError(f"nothing to uninstall on {system or 'this platform'}")
