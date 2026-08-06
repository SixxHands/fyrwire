package preset

import "fmt"

type PresetData struct {
	Key      string
	Label    string
	Port     int
	Protocol string
	Record   string
	Service  string
	Follows  string
	Note     string
	Group    string
}

var Groups = []string{"auto", "setup-only", "browser", "apps", "games", "remote", "mail", "generic"}

var All = []PresetData{
	{Key: "minecraft", Label: "Minecraft (Java)", Port: 25565, Protocol: "tcp", Record: "srv",
		Service: "minecraft", Follows: "srv", Group: "auto",
		Note: "Fully automatic. Players should enter the bare hostname because the client skips _minecraft._tcp when ':port' is typed."},
	{Key: "mumble", Label: "Mumble (voice)", Port: 64738, Protocol: "tcp", Record: "srv",
		Service: "mumble", Follows: "srv", Group: "auto",
		Note: "SRV covers TCP only and Mumble sends voice UDP to the same port number. Both TCP and UDP need the same port or voice falls back to TCP."},
	{Key: "teamspeak3", Label: "TeamSpeak 3", Port: 9987, Protocol: "udp", Record: "srv",
		Service: "ts3", Follows: "srv", Group: "auto",
		Note: "Automatic for the voice port only (_ts3._udp). File transfer (30033) and ServerQuery (10011) are not covered; enter the bare hostname."},
	{Key: "factorio", Label: "Factorio", Port: 34197, Protocol: "udp", Record: "srv",
		Service: "factorio", Follows: "srv", Group: "auto",
		Note: "Publish one SRV record (the client uses the first and ignores priority/weight). Wildcard CNAMEs break lookup, and Switch has no SRV support."},
	{Key: "xmpp-client", Label: "XMPP (client-to-server)", Port: 5222, Protocol: "tcp", Record: "srv",
		Service: "xmpp-client", Follows: "srv", Group: "auto",
		Note: "Automatic per connection; TLS cert must match the domain users type, not node.example.com. Manual host/port overrides skip SRV."},
	{Key: "xmpp-server", Label: "XMPP (federation)", Port: 5269, Protocol: "tcp", Record: "srv",
		Service: "xmpp-server", Follows: "srv", Group: "auto",
		Note: "Peers re-resolve on TTL and will not fall back to 5269 once an SRV answers, so keep TTL at 60s."},
	{Key: "matrix-fed", Label: "Matrix (federation)", Port: 8448, Protocol: "tcp", Record: "srv",
		Service: "matrix-fed", Follows: "srv", Group: "auto",
		Note: "Federation only (clients ignore it). A /.well-known/matrix/server file overrides SRV, so remove it or keep it in sync."},
	{Key: "sip-udp", Label: "SIP over UDP", Port: 5060, Protocol: "udp", Record: "srv",
		Service: "sip", Follows: "srv", Group: "auto",
		Note: "Any explicit port in the SIP URI (including a prefilled 5060) disables SRV lookup; use a portless sip:user@example.com."},
	{Key: "sip-tcp", Label: "SIP over TCP", Port: 5060, Protocol: "tcp", Record: "srv",
		Service: "sip", Follows: "srv", Group: "auto",
		Note: "Same caveats as sip-udp. PJSIP stacks only do SRV when a nameserver is explicitly configured."},
	{Key: "sips", Label: "SIP over TLS (SIPS)", Port: 5061, Protocol: "tcp", Record: "srv",
		Service: "sips", Follows: "srv", Group: "auto",
		Note: "Cert is checked against the SIPS URI domain, not the SRV target. Signaling follows rotations, but RTP audio ports need separate forwarding."},
	{Key: "kerberos-udp", Label: "Kerberos KDC (UDP)", Port: 88, Protocol: "udp", Record: "srv",
		Service: "kerberos", Follows: "srv", Group: "auto",
		Note: "On by default in MIT krb5, but 'kdc =' in krb5.conf or a URI record disables it, and many clients only try port 88."},
	{Key: "kerberos-tcp", Label: "Kerberos KDC (TCP)", Port: 88, Protocol: "tcp", Record: "srv",
		Service: "kerberos", Follows: "srv", Group: "auto",
		Note: "RFC 4120 expects both _kerberos._udp and _kerberos._tcp to exist together; add this alongside kerberos-udp."},
	{Key: "caldav", Label: "CalDAV (calendar)", Port: 443, Protocol: "tcp", Record: "srv",
		Service: "caldavs", Follows: "srv-setup", Group: "setup-only",
		Note: "DAVx5 and Thunderbird read SRV once when adding the account; later port rotations require re-adding the account."},
	{Key: "carddav", Label: "CardDAV (contacts)", Port: 443, Protocol: "tcp", Record: "srv",
		Service: "carddavs", Follows: "srv-setup", Group: "setup-only",
		Note: "Same setup-only behavior as caldav. Publish TXT 'path=/remote.php/dav/' at the same owner name and serve HTTPS with a valid cert."},
	{Key: "web", Label: "Website / HTTPS service", Port: 443, Protocol: "tcp", Record: "https",
		Service: "https", Follows: "manual", Group: "browser",
		Note: "Chrome ignores non-443 ports in HTTPS records; only Firefox with DoH honors port=. Use a fixed-port reverse proxy or tunnel for browsers."},
	{Key: "web-plain", Label: "HTTP (no TLS)", Port: 80, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "browser",
		Note: "No HTTPS record is published (it would force browsers onto https). Plain HTTP has no port discovery record."},
	{Key: "api", Label: "API / webhook endpoint", Port: 8080, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "browser",
		Note: "Point clients at the host:port from `fyrwire status`; nothing discovers the port automatically."},
	{Key: "jellyfin", Label: "Jellyfin / Emby", Port: 8096, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Clients save the full http://node.example.com:PORT URL and never re-check DNS. UDP 7359 autodiscovery is LAN-only."},
	{Key: "plex", Label: "Plex Media Server", Port: 32400, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Usually redundant: set 'Manually specify public port' in Plex and clients will find it via plex.direct."},
	{Key: "nextcloud", Label: "Nextcloud", Port: 443, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Sync clients store the full URL. trusted_domains survives port changes, but overwritehost/overwriteport/overwrite.cli.url need updating."},
	{Key: "immich", Label: "Immich", Port: 2283, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Server Endpoint URL takes host:port; the mobile app does not do DNS SVCB/HTTPS lookups."},
	{Key: "navidrome", Label: "Navidrome / Subsonic", Port: 4533, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Subsonic clients store the base URL literally and have no port discovery."},
	{Key: "audiobookshelf", Label: "Audiobookshelf", Port: 13378, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Clients take http://host:PORT directly; SSO requires https with a valid certificate."},
	{Key: "home-assistant", Label: "Home Assistant", Port: 8123, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Set node.example.com:PORT as External URL in the companion app; mDNS discovery does not cross VPNs."},
	{Key: "actual-budget", Label: "Actual Budget", Port: 5006, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "The sync server URL is stored per device, so each client needs updating after a rotation."},
	{Key: "paperless-ngx", Label: "Paperless-ngx", Port: 8000, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Browser-first with no port discovery. Mobile apps take host:port, and PAPERLESS_URL / allowed hosts must match."},
	{Key: "vaultwarden", Label: "Vaultwarden", Port: 8000, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Bitwarden clients require https://node.example.com:PORT with a valid cert (certs are not port-bound, so rotations keep TLS valid)."},
	{Key: "gitea-forgejo", Label: "Gitea / Forgejo", Port: 3000, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Poor fit for rotating ports: every git remote plus ROOT_URL/SSH_PORT changes on rotation. Use a fixed-port tunnel instead."},
	{Key: "frigate", Label: "Frigate NVR", Port: 8971, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Needs multiple ports (8971 UI, 8554 RTSP, 8555 tcp+udp WebRTC), so one lease only covers the web UI."},
	{Key: "foundry-vtt", Label: "Foundry Virtual Tabletop", Port: 30000, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Browser clients cannot follow rotating ports; put Foundry behind a fixed-port reverse proxy."},
	{Key: "syncthing", Label: "Syncthing", Port: 22000, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Mostly redundant since Syncthing's own discovery/relays track peers. Only pinned tcp://host:PORT addresses need updating."},
	{Key: "qbittorrent", Label: "qBittorrent (WebUI)", Port: 8080, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Covers the WebUI port only; update Settings > Connection > Listening Port separately for peer traffic."},
	{Key: "matrix-client", Label: "Matrix (client access)", Port: 443, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Clients read /.well-known/matrix/client over HTTPS rather than DNS SRV; put the client API behind a fixed-port proxy."},
	{Key: "minecraft-bedrock", Label: "Minecraft (Bedrock)", Port: 19132, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "No SRV support (an SRV record can break Bedrock connections). Players enter the port in Add Server."},
	{Key: "valheim", Label: "Valheim", Port: 2456, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Needs game+1 query port (2456 and 2457) for the Steam browser; direct join works on one port, or use -crossplay."},
	{Key: "ark", Label: "ARK: Survival Evolved / Ascended", Port: 7777, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Evolved needs 7777/7778 plus 27015 query; Ascended needs 7777. Ports are entered manually and Steam favorites go stale."},
	{Key: "rust", Label: "Rust", Port: 28015, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Players join with `client.connect host:PORT` in the F1 console; set +server.queryport explicitly."},
	{Key: "7dtd", Label: "7 Days to Die", Port: 26900, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Needs four contiguous ports (26900-26903), so a single leased port cannot serve it."},
	{Key: "palworld", Label: "Palworld", Port: 8211, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Only 8211/udp is needed to play (-queryport is separate), but players must re-enter the port after a rotation."},
	{Key: "satisfactory", Label: "Satisfactory (dedicated)", Port: 7777, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "External port must match the port the server binds, so redirected/rotating ports break it."},
	{Key: "project-zomboid", Label: "Project Zomboid", Port: 16261, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Needs two ports (16261 and 16262). Both are configurable independently, but players must re-enter them on rotation."},
	{Key: "terraria", Label: "Terraria / tModLoader", Port: 7777, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Join via IP takes host and port separately; the .NET client does not look up SRV records."},
	{Key: "source-engine", Label: "Source engine (CS2, TF2, GMod)", Port: 27015, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Game traffic and A2S query share one port, but players must run `connect host:PORT` and favorites go stale."},
	{Key: "ssh", Label: "SSH", Port: 22, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "OpenSSH does not support SRV records; use `ssh -p PORT` or update ~/.ssh/config."},
	{Key: "wireguard", Label: "WireGuard", Port: 51820, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "WireGuard re-resolves the Endpoint hostname, but the port in 'Endpoint = host:port' is static per peer."},
	{Key: "rdp", Label: "Remote Desktop (RDP)", Port: 3389, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "RDP clients take host:port literally; hand out the port after rotations."},
	{Key: "vnc", Label: "VNC", Port: 5900, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "Viewers take host:port (watch out for the 'host:1' = 5901 shorthand)."},
	{Key: "ldap", Label: "LDAP directory", Port: 389, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "OpenLDAP only uses _ldap._tcp for hostless URIs, and Active Directory uses separate _msdcs names."},
	{Key: "coturn", Label: "STUN / TURN (coturn)", Port: 3478, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "Browsers resolve ICE URIs with A/AAAA only, so stun:/turn: URIs need a literal port, and TURN allocates extra relay ports."},
	{Key: "ftp", Label: "FTP", Port: 21, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "Passive FTP negotiates a second data port in-band that DNS cannot express; use SFTP over SSH instead."},
	{Key: "imaps", Label: "IMAP over TLS", Port: 993, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "mail",
		Note: "Thunderbird and Outlook use autoconfig instead of _imaps._tcp, and clients that read it only do so during setup."},
	{Key: "submission", Label: "SMTP submission", Port: 587, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "mail",
		Note: "Same as imaps. Inbound MX records have no port field, so receiving mail cannot use a rotating port."},
	{Key: "generic-tcp", Label: "Any TCP service", Port: 0, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "generic",
		Note: "No port discovery; FyrWire updates the A record and `fyrwire status` prints the current host:port."},
	{Key: "generic-udp", Label: "Any UDP service", Port: 0, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "generic",
		Note: "No port discovery; FyrWire updates the A record and `fyrwire status` prints the current host:port."},
	{Key: "custom-srv", Label: "Custom SRV service (advanced)", Port: 0, Protocol: "tcp", Record: "srv",
		Service: "", Follows: "srv", Group: "generic",
		Note: "Only use this if you confirmed your client actually queries SRV records."},
}

var byKey = func() map[string]PresetData {
	m := make(map[string]PresetData, len(All))
	for _, p := range All {
		m[p.Key] = p
	}
	return m
}()

func Get(key string) (PresetData, bool) {
	p, ok := byKey[key]
	return p, ok
}

func ByGroup(group string) []PresetData {
	var out []PresetData
	for _, p := range All {
		if p.Group == group {
			out = append(out, p)
		}
	}
	return out
}

func CheckServiceLabel(name string) error {
	if name == "" {
		return errf("service label is empty")
	}
	if len(name) > 15 {
		return errf("service label %q is %d characters; RFC 6335 allows 15", name, len(name))
	}
	hasLetter := false
	for i := 0; i < len(name); i++ {
		if err := checkLabelChar(name, i, &hasLetter); err != nil {
			return err
		}
	}
	if !hasLetter {
		return errf("service label %q must contain at least one letter", name)
	}
	return nil
}

func checkLabelChar(name string, i int, hasLetter *bool) error {
	c := name[i]
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		*hasLetter = true
	case c >= '0' && c <= '9':
	case c == '-':
		if i == 0 || i == len(name)-1 {
			return errf("service label %q may not start or end with a hyphen", name)
		}
		if name[i-1] == '-' {
			return errf("service label %q has two hyphens in a row", name)
		}
	default:
		return errf("service label %q contains %q; only letters, digits and hyphens are allowed",
			name, string(c))
	}
	return nil
}

func errf(format string, a ...any) error {
	return &serviceError{msg: fmt.Sprintf(format, a...)}
}

type serviceError struct{ msg string }

func (e *serviceError) Error() string { return e.msg }

func init() {
	registerLookup(func(key string) (record, protocol string, ok bool) {
		p, found := byKey[key]
		if !found {
			return "", "", false
		}
		return p.Record, p.Protocol, true
	})
}
