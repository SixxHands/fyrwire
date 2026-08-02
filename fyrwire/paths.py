from __future__ import annotations

import os
import platform

APP = "fyrwire"
APP_DISPLAY = "FyrWire"


def _home() -> str:
    return os.path.expanduser("~")


def config_dir() -> str:
    if os.environ.get("XDG_CONFIG_HOME"):
        return os.path.join(os.environ["XDG_CONFIG_HOME"], APP)
    system = platform.system()
    if system == "Windows":
        base = os.environ.get("APPDATA") or os.path.join(
            _home(), "AppData", "Roaming")
        return os.path.join(base, APP_DISPLAY)
    if system == "Darwin":
        return os.path.join(_home(), "Library", "Application Support", APP_DISPLAY)
    return os.path.join(_home(), ".config", APP)


def state_dir() -> str:
    if os.environ.get("XDG_STATE_HOME"):
        return os.path.join(os.environ["XDG_STATE_HOME"], APP)
    system = platform.system()
    if system == "Windows":
        base = os.environ.get("LOCALAPPDATA") or os.path.join(
            _home(), "AppData", "Local")
        return os.path.join(base, APP_DISPLAY)
    if system == "Darwin":
        return os.path.join(_home(), "Library", "Application Support", APP_DISPLAY)
    return os.path.join(_home(), ".local", "state", APP)


def config_file() -> str:
    return os.environ.get("FYRWIRE_CONFIG") or os.path.join(
        config_dir(), "fyrwire.toml")


def state_file() -> str:
    return os.environ.get("FYRWIRE_STATE") or os.path.join(
        state_dir(), "state.json")


def service_dir() -> str:
    system = platform.system()
    if system == "Darwin":
        return os.path.join(_home(), "Library", "LaunchAgents")
    if system == "Windows":
        return ""
    return os.path.join(_home(), ".config", "systemd", "user")


def legacy_locations() -> list[tuple[str, str]]:
    out = []
    old_cfg = os.path.join(_home(), ".config", APP, "fyrwire.toml")
    old_state = os.path.join(_home(), ".local", "state", APP, "state.json")
    if os.path.abspath(old_cfg) != os.path.abspath(config_file()):
        out.append((old_cfg, config_file()))
    if os.path.abspath(old_state) != os.path.abspath(state_file()):
        out.append((old_state, state_file()))
    return out


def describe() -> list[tuple[str, str]]:
    return [("config", config_file()), ("state", state_file())]
