from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class PresetData:
    key: str
    label: str
    port: int
    protocol: str
    record: str
    service: str
    follows: str
    note: str = ""

    def srv_name(self, zone: str) -> str:
        return f"_{self.service}._{self.protocol}.{zone}"


PRESETS: dict[str, PresetData] = {
    "minecraft": PresetData(
        "minecraft", "Minecraft (Java)", 25565, "tcp", "srv", "minecraft", "srv",
        "Java clients resolve _minecraft._tcp and use the port. This is the case "
        "the whole design was built around, and the only one that is fully automatic."),
    "minecraft-bedrock": PresetData(
        "minecraft-bedrock", "Minecraft (Bedrock)", 19132, "udp", "srv", "minecraft",
        "manual",
        "Bedrock is UDP and does not use SRV; players enter host and port."),
    "web": PresetData(
        "web", "Website / HTTP service", 443, "tcp", "https", "https", "https",
        "Browsers may ignore the port and use 443 — see the README. For a public "
        "website a reverse proxy or tunnel is the reliable answer."),
    "web-plain": PresetData(
        "web-plain", "HTTP (no TLS)", 80, "tcp", "https", "http", "https",
        "Same caveat as `web`, and no TLS."),
    "api": PresetData(
        "api", "API / webhook endpoint", 8080, "tcp", "https", "https", "manual",
        "Clients you control can be pointed at host:port, which makes this far "
        "more practical than a browser-facing site."),
    "ssh": PresetData(
        "ssh", "SSH", 22, "tcp", "srv", "ssh", "manual",
        "OpenSSH does not resolve SRV. Use `ssh -p` or a Host block in "
        "~/.ssh/config; FyrWire keeps the address correct."),
    "valheim": PresetData(
        "valheim", "Valheim", 2456, "udp", "srv", "valheim", "manual",
        "UDP, and also uses 2457. Players enter host and port."),
    "terraria": PresetData(
        "terraria", "Terraria", 7777, "tcp", "srv", "terraria", "manual",
        "No SRV support in the client."),
    "generic-tcp": PresetData(
        "generic-tcp", "Any TCP service", 0, "tcp", "srv", "svc", "manual", ""),
    "generic-udp": PresetData(
        "generic-udp", "Any UDP service", 0, "udp", "srv", "svc", "manual", ""),
}

FOLLOWS_HELP = {
    "srv": "automatic — the client resolves the SRV record and follows the port",
    "https": "MAYBE — client must implement RFC 9460's `port` SvcParam, and many "
             "do not; they fall back to the default port",
    "manual": "not automatic — hand out the host:port from `fyrwire status`",
}


def get(key: str) -> PresetData | None:
    return PRESETS.get(key)


def listing() -> list[PresetData]:
    return list(PRESETS.values())
