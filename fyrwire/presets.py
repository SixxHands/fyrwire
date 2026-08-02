from __future__ import annotations

import re
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
    group: str = "manual"

    def srv_name(self, zone: str) -> str:
        if self.record != "srv":
            raise ValueError(
                f"preset {self.key!r} publishes record={self.record!r}, "
                f"it has no SRV name")
        if self.protocol not in ("tcp", "udp"):
            raise ValueError(
                f"preset {self.key!r}: SRV protocol must be tcp or udp, "
                f"not {self.protocol!r}")
        return f"_{self.service}._{self.protocol}.{zone}"


GROUPS: dict[str, str] = {
    "auto":       "Automatic — the client resolves SRV on every connection",
    "setup-only": "SRV, but read ONCE at account setup (breaks on rotation)",
    "browser":    "Browser-facing — no client follows the port; use a proxy",
    "apps":       "Self-hosted apps — you paste host:port into the app once",
    "games":      "Games without SRV — the player types the port",
    "remote":     "Remote access and infrastructure — port given by hand",
    "mail":       "Email — see the note, this mostly cannot work",
    "generic":    "Anything else",
}


PRESETS: dict[str, PresetData] = {


    "minecraft": PresetData(
        "minecraft", "Minecraft (Java)", 25565, "tcp", "srv", "minecraft", "srv",
        "Fully automatic — but tell players to type the bare hostname: the "
        "client only looks up _minecraft._tcp when no ':port' was typed.",
        "auto"),
    "mumble": PresetData(
        "mumble", "Mumble (voice)", 64738, "tcp", "srv", "mumble", "srv",
        "SRV covers TCP only and Mumble sends voice UDP to the same port number "
        "as the control connection — the lease MUST give the same number for "
        "both or voice silently degrades to TCP tunnelling.",
        "auto"),
    "teamspeak3": PresetData(
        "teamspeak3", "TeamSpeak 3", 9987, "udp", "srv", "ts3", "srv",
        "Automatic for the voice port only (the label is _ts3._udp, never "
        "_tcp); file transfer 30033 and ServerQuery 10011 are not covered, and "
        "players should enter the bare hostname.",
        "auto"),
    "factorio": PresetData(
        "factorio", "Factorio", 34197, "udp", "srv", "factorio", "srv",
        "Publish exactly one SRV — the client uses the first record and ignores "
        "priority/weight; a wildcard CNAME in the zone breaks the lookup, and "
        "the Nintendo Switch build has no SRV support.",
        "auto"),
    "xmpp-client": PresetData(
        "xmpp-client", "XMPP (client-to-server)", 5222, "tcp", "srv",
        "xmpp-client", "srv",
        "Automatic per connection; your TLS cert must match the domain users "
        "type, not node.example.com — but a client with a hand-entered server "
        "and port skips the lookup entirely.",
        "auto"),
    "xmpp-server": PresetData(
        "xmpp-server", "XMPP (federation)", 5269, "tcp", "srv",
        "xmpp-server", "srv",
        "Peers re-resolve on TTL and will NOT fall back to 5269 once an SRV has "
        "answered, so keep the TTL at 60s — a stale port is a hard outage, not "
        "a slow one.",
        "auto"),
    "matrix-fed": PresetData(
        "matrix-fed", "Matrix (federation)", 8448, "tcp", "srv",
        "matrix-fed", "srv",
        "Federation only — Element and other clients never read it; and a "
        "/.well-known/matrix/server file WINS over this record, so delete it or "
        "keep it in sync. (_matrix._tcp is the deprecated old name.)",
        "auto"),
    "sip-udp": PresetData(
        "sip-udp", "SIP over UDP", 5060, "udp", "srv", "sip", "srv",
        "Any explicit port in the SIP URI — including a prefilled '5060' in a "
        "softphone's port box — disables the SRV lookup, so hand out a portless "
        "sip:user@example.com and clear that field.",
        "auto"),
    "sip-tcp": PresetData(
        "sip-tcp", "SIP over TCP", 5060, "tcp", "srv", "sip", "srv",
        "Same caveats as sip-udp; PJSIP-based stacks (pjsua, Asterisk res_pjsip) "
        "only do SRV when a nameserver is configured, otherwise they resolve "
        "A-only and ignore the record without an error.",
        "auto"),
    "sips": PresetData(
        "sips", "SIP over TLS (SIPS)", 5061, "tcp", "srv", "sips", "srv",
        "Cert is checked against the domain in the SIPS URI, not the SRV target; "
        "note that signalling follows the rotation but RTP audio ports are "
        "negotiated in SDP and need their own forwarding.",
        "auto"),
    "kerberos-udp": PresetData(
        "kerberos-udp", "Kerberos KDC (UDP)", 88, "udp", "srv", "kerberos", "srv",
        "On by default in MIT krb5, but any 'kdc =' line in krb5.conf or a URI "
        "record suppresses it and the RFC itself warns many clients only ever "
        "try port 88 — do not rely on this for a rotating port.",
        "auto"),
    "kerberos-tcp": PresetData(
        "kerberos-tcp", "Kerberos KDC (TCP)", 88, "tcp", "srv", "kerberos", "srv",
        "RFC 4120 requires BOTH _kerberos._udp and _kerberos._tcp to be present "
        "if either is — add this alongside kerberos-udp, never on its own.",
        "auto"),


    "caldav": PresetData(
        "caldav", "CalDAV (calendar)", 443, "tcp", "srv", "caldavs", "srv-setup",
        "DAVx5 and Thunderbird read this once when the account is added: the "
        "first setup finds the port on its own, then every later rotation breaks "
        "the account until it is deleted and re-added.",
        "setup-only"),
    "carddav": PresetData(
        "carddav", "CardDAV (contacts)", 443, "tcp", "srv", "carddavs", "srv-setup",
        "Same one-shot behaviour as caldav; publish TXT 'path=/remote.php/dav/' "
        "at the SAME owner name as the SRV, at the domain users actually type "
        "(usually the apex), and serve HTTPS with a valid cert.",
        "setup-only"),


    "web": PresetData(
        "web", "Website / HTTPS service", 443, "tcp", "https", "https", "manual",
        "Chrome discards an HTTPS record whose port differs and connects to 443 "
        "anyway; only Firefox with DoH on honours port=. Put a fixed-port "
        "reverse proxy or tunnel in front — that is the reliable answer.",
        "browser"),
    "web-plain": PresetData(
        "web-plain", "HTTP (no TLS)", 80, "tcp", "none", "", "manual",
        "We deliberately publish no HTTPS record here — its presence tells "
        "browsers to switch to https, the opposite of what you asked for. "
        "Nothing carries a port for cleartext HTTP.",
        "browser"),
    "api": PresetData(
        "api", "API / webhook endpoint", 8080, "tcp", "none", "", "manual",
        "Clients you control can be pointed at the host:port from "
        "`fyrwire status`, which makes this far more practical than a browser — "
        "but nothing discovers it for you.",
        "browser"),


    "jellyfin": PresetData(
        "jellyfin", "Jellyfin / Emby", 8096, "tcp", "none", "", "manual",
        "Every client takes a full URL so http://node.example.com:PORT works, "
        "but the saved URL never re-checks DNS, and the 7359/udp autodiscovery "
        "is a LAN broadcast that cannot cross the VPN.",
        "apps"),
    "plex": PresetData(
        "plex", "Plex Media Server", 32400, "tcp", "none", "", "manual",
        "Mostly redundant: set 'Manually specify public port' to the forwarded "
        "port and Plex republishes it to plex.tv itself; clients connect via "
        "plex.direct and never see your DNS at all.",
        "apps"),
    "nextcloud": PresetData(
        "nextcloud", "Nextcloud", 443, "tcp", "none", "", "manual",
        "Sync clients store the URL you type and never re-resolve a port; "
        "trusted_domains survives a port change, but overwritehost / "
        "overwriteport / overwrite.cli.url need updating each rotation.",
        "apps"),
    "immich": PresetData(
        "immich", "Immich", 2283, "tcp", "none", "", "manual",
        "Server Endpoint URL takes host:port; the maintainers explicitly "
        "rejected DNS SVCB/HTTPS lookups in the mobile app, so nothing here "
        "will ever follow a port automatically.",
        "apps"),
    "navidrome": PresetData(
        "navidrome", "Navidrome / Subsonic", 4533, "tcp", "none", "", "manual",
        "The Subsonic API defines no discovery layer at all — Symfonium, DSub, "
        "Feishin and the rest simply store the base URL you type.",
        "apps"),
    "audiobookshelf": PresetData(
        "audiobookshelf", "Audiobookshelf", 13378, "tcp", "none", "", "manual",
        "The app's own placeholder is http://host:13378, so a host:port is "
        "expected — but if you enable SSO the endpoint must be https with a "
        "valid certificate.",
        "apps"),
    "home-assistant": PresetData(
        "home-assistant", "Home Assistant", 8123, "tcp", "none", "", "manual",
        "Put node.example.com:PORT in the companion app's External URL; it "
        "switches to the Internal URL on your home wifi, and its mDNS discovery "
        "is multicast-only so it cannot cross the VPN.",
        "apps"),
    "actual-budget": PresetData(
        "actual-budget", "Actual Budget", 5006, "tcp", "none", "", "manual",
        "The sync-server URL is stored per device, so every phone and desktop "
        "needs re-pointing after a rotation.",
        "apps"),
    "paperless-ngx": PresetData(
        "paperless-ngx", "Paperless-ngx", 8000, "tcp", "none", "", "manual",
        "Browser-first, so no port discovery; third-party mobile apps do take "
        "host:port, and PAPERLESS_URL / allowed hosts must list the name you "
        "publish.",
        "apps"),
    "vaultwarden": PresetData(
        "vaultwarden", "Vaultwarden", 8000, "tcp", "none", "", "manual",
        "Bitwarden clients refuse plain http — the Server URL must be "
        "https://node.example.com:PORT with a valid cert; the port itself is not "
        "covered by the certificate, so a rotation does not break TLS.",
        "apps"),
    "gitea-forgejo": PresetData(
        "gitea-forgejo", "Gitea / Forgejo", 3000, "tcp", "none", "", "manual",
        "Weakest fit in the list: a rotating port rewrites every git remote in "
        "every clone on every machine, and ROOT_URL/SSH_PORT must change too — "
        "use a fixed-port tunnel for git.",
        "apps"),
    "frigate": PresetData(
        "frigate", "Frigate NVR", 8971, "tcp", "none", "", "manual",
        "Needs several ports at once (8971 UI, 8554 RTSP, 8555 tcp+udp WebRTC), "
        "so one forwarded port only gets you the web UI; go2rtc's WebRTC "
        "candidates need the external address set separately.",
        "apps"),
    "foundry-vtt": PresetData(
        "foundry-vtt", "Foundry Virtual Tabletop", 30000, "tcp", "none", "",
        "manual",
        "The client is a browser, so nothing can follow the port — front Foundry "
        "with a fixed-port reverse proxy and give players a plain https URL.",
        "apps"),
    "syncthing": PresetData(
        "syncthing", "Syncthing", 22000, "tcp", "none", "", "manual",
        "Largely redundant — global discovery and relays already track you; only "
        "a peer with a pinned tcp://node.example.com:PORT needs editing, and "
        "22000 is TCP and UDP on the same number.",
        "apps"),
    "qbittorrent": PresetData(
        "qbittorrent", "qBittorrent (WebUI)", 8080, "tcp", "none", "", "manual",
        "DNS is the small half here: what matters is pushing the forwarded port "
        "into Settings > Connection > Listening Port. This preset is the WebUI, "
        "which is a separate port.",
        "apps"),
    "matrix-client": PresetData(
        "matrix-client", "Matrix (client access)", 443, "tcp", "none", "",
        "manual",
        "Clients read /.well-known/matrix/client over HTTPS, not DNS — the port "
        "lives inside that file's base_url, so put the client API behind a "
        "fixed-port proxy.",
        "apps"),


    "minecraft-bedrock": PresetData(
        "minecraft-bedrock", "Minecraft (Bedrock)", 19132, "udp", "none", "",
        "manual",
        "No SRV support — and worse, a domain that resolves to an SRV record can "
        "break Bedrock connections outright; players type the port in the Add "
        "Server dialog.",
        "games"),
    "valheim": PresetData(
        "valheim", "Valheim", 2456, "udp", "none", "", "manual",
        "Needs the query port at game+1 (2456 and 2457), so non-adjacent leased "
        "ports make the server invisible in the Steam browser even when direct "
        "join works; -crossplay avoids ports entirely.",
        "games"),
    "ark": PresetData(
        "ark", "ARK: Survival Evolved / Ascended", 7777, "udp", "none", "",
        "manual",
        "Evolved also needs gameport+1 (7777/7778) plus 27015 query; Ascended "
        "needs only 7777 — either way the port is typed by hand and Steam "
        "favourites go stale.",
        "games"),
    "rust": PresetData(
        "rust", "Rust", 28015, "udp", "none", "", "manual",
        "Players use `client.connect host:PORT` in the F1 console; the query "
        "port default differs between builds, so set +server.queryport "
        "explicitly rather than assuming 28017.",
        "games"),
    "7dtd": PresetData(
        "7dtd", "7 Days to Die", 26900, "tcp", "none", "", "manual",
        "Needs a contiguous four-port block (26900-26903) derived from "
        "ServerPort — a single leased port cannot serve this at all.",
        "games"),
    "palworld": PresetData(
        "palworld", "Palworld", 8211, "udp", "none", "", "manual",
        "Only 8211/udp is needed to play and -queryport is independently "
        "settable, so one lease is enough — but the player retypes the port on "
        "every rotation.",
        "games"),
    "satisfactory": PresetData(
        "satisfactory", "Satisfactory (dedicated)", 7777, "udp", "none", "",
        "manual",
        "Incompatible with a rotating lease: the official wiki states the "
        "external port must equal the port the server binds, so a redirected "
        "port breaks it regardless of DNS.",
        "games"),
    "project-zomboid": PresetData(
        "project-zomboid", "Project Zomboid", 16261, "udp", "none", "", "manual",
        "Two ports must both be open (16261 and 16262); they are independently "
        "configurable, so two unrelated leases work — the player still retypes "
        "both.",
        "games"),
    "terraria": PresetData(
        "terraria", "Terraria / tModLoader", 7777, "tcp", "none", "", "manual",
        "Join via IP takes host and port in separate fields, and the .NET "
        "resolver the client uses cannot return SRV records at all.",
        "games"),
    "source-engine": PresetData(
        "source-engine", "Source engine (CS2, TF2, GMod)", 27015, "udp", "none",
        "", "manual",
        "Game traffic and the A2S query share one port, so a single lease covers "
        "everything — but players must `connect host:PORT` each time and saved "
        "favourites and steam:// links go stale.",
        "games"),


    "ssh": PresetData(
        "ssh", "SSH", 22, "tcp", "none", "", "manual",
        "OpenSSH has no SRV support (SSHFP is unrelated and carries no port) — "
        "use `ssh -p PORT`, or a Host block with Port in ~/.ssh/config that you "
        "update when the port moves.",
        "remote"),
    "wireguard": PresetData(
        "wireguard", "WireGuard", 51820, "udp", "none", "", "manual",
        "wg re-resolves the Endpoint hostname, so a changing ADDRESS is followed "
        "automatically — but the port in 'Endpoint = host:port' is literal and "
        "must be edited on every peer.",
        "remote"),
    "rdp": PresetData(
        "rdp", "Remote Desktop (RDP)", 3389, "tcp", "none", "", "manual",
        "mstsc and every other RDP client take host:port literally; hand the "
        "port out.",
        "remote"),
    "vnc": PresetData(
        "vnc", "VNC", 5900, "tcp", "none", "", "manual",
        "Viewers take host:port, and the 'host:1' shorthand means 5901 — which "
        "makes a rotating five-digit port unusually easy to mistype.",
        "remote"),
    "ldap": PresetData(
        "ldap", "LDAP directory", 389, "tcp", "none", "", "manual",
        "A registered service name with no client discovery: OpenLDAP only tries "
        "_ldap._tcp for a hostless URI like ldap:///dc=example,dc=com, and Active "
        "Directory uses different names under _msdcs.",
        "remote"),
    "coturn": PresetData(
        "coturn", "STUN / TURN (coturn)", 3478, "udp", "none", "", "manual",
        "Browsers resolve ICE server URIs with getaddrinfo — A/AAAA only — so "
        "stun:/turn: URIs must carry the port literally, and TURN allocates "
        "relay ports beyond the listener anyway.",
        "remote"),
    "ftp": PresetData(
        "ftp", "FTP", 21, "tcp", "none", "", "manual",
        "Passive mode negotiates a second data port in-band that no DNS record "
        "can express — use SFTP over the SSH mapping instead.",
        "remote"),


    "imaps": PresetData(
        "imaps", "IMAP over TLS", 993, "tcp", "none", "", "manual",
        "RFC 6186 defines _imaps._tcp, but Thunderbird and Outlook do not "
        "implement it (they use their own autoconfig), and where it exists it is "
        "setup-time only.",
        "mail"),
    "submission": PresetData(
        "submission", "SMTP submission", 587, "tcp", "none", "", "manual",
        "Same as imaps — and note inbound mail is routed by MX, which has no "
        "port field at all, so a rotating port for receiving mail can never be "
        "made to work.",
        "mail"),


    "generic-tcp": PresetData(
        "generic-tcp", "Any TCP service", 0, "tcp", "none", "", "manual",
        "No port discovery — FyrWire keeps the A record correct and "
        "`fyrwire status` prints the current host:port.",
        "generic"),
    "generic-udp": PresetData(
        "generic-udp", "Any UDP service", 0, "udp", "none", "", "manual",
        "No port discovery — FyrWire keeps the A record correct and "
        "`fyrwire status` prints the current host:port.",
        "generic"),
    "custom-srv": PresetData(
        "custom-srv", "Custom SRV service (advanced)", 0, "tcp", "srv", "", "srv",
        "Only pick this if you have CHECKED that your client really resolves SRV "
        "— see the checklist `fyrwire presets --custom`; guessing wrong sends "
        "people to the old port with no error.",
        "generic"),
}

FOLLOWS_HELP = {
    "srv":       "automatic — the client resolves the SRV record on every "
                 "connection and follows the port",
    "srv-setup": "ONCE — the client reads the SRV record when the account is "
                 "added, then stores the port; later rotations break it",
    "manual":    "not automatic — hand out the host:port from `fyrwire status`",
}


def get(key: str) -> PresetData | None:
    return PRESETS.get(key)


def listing() -> list[PresetData]:
    order = list(GROUPS)
    return sorted(PRESETS.values(), key=lambda p: order.index(p.group))


def by_group() -> list[tuple[str, str, list[PresetData]]]:
    out = []
    for key, heading in GROUPS.items():
        members = [p for p in PRESETS.values() if p.group == key]
        if members:
            out.append((key, heading, members))
    return out


_SERVICE_RE = re.compile(r"^(?![0-9-]+$)[A-Za-z0-9](?:[A-Za-z0-9-]{0,13}[A-Za-z0-9])?$")


def validate_custom(service: str, protocol: str, port: int,
                    target: str, owner_zone: str) -> list[str]:
    errs: list[str] = []

    if service.startswith("_"):
        errs.append("service must not start with '_' — FyrWire adds the "
                    "underscores; write 'minecraft', not '_minecraft'")
    if "." in service:
        errs.append("service is a single label — no dots, and no '._tcp' suffix")
    if not _SERVICE_RE.match(service.lstrip("_")):
        errs.append("service must be 1-15 characters of letters, digits and "
                    "hyphens, contain a letter, and not start or end with '-'")
    if "--" in service:
        errs.append("service must not contain two adjacent hyphens")

    if protocol not in ("tcp", "udp"):
        errs.append("protocol must be 'tcp' or 'udp' — a service on both needs "
                    "two entries, one per protocol")
    if not 1 <= port <= 65535:
        errs.append("port must be between 1 and 65535")

    if not target or target == ".":
        errs.append("target must be a hostname with an A/AAAA record; '.' means "
                    "'service unavailable'")
    elif not target.rstrip(".").count("."):
        errs.append("target must be a fully-qualified name (node.example.com), "
                    "not a bare label")

    if owner_zone and target.rstrip(".") == owner_zone.rstrip("."):
        errs.append("SRV target must not be the same name users type — point it "
                    "at the A record for this machine (node.example.com)")
    return errs
