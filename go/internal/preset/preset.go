package preset

import "fmt"

var sprintf = fmt.Sprintf

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
		Note: "Fully automatic \u2014 but tell players to type the bare hostname: the client only looks up _minecraft._tcp when no ':port' was typed."},
	{Key: "mumble", Label: "Mumble (voice)", Port: 64738, Protocol: "tcp", Record: "srv",
		Service: "mumble", Follows: "srv", Group: "auto",
		Note: "SRV covers TCP only and Mumble sends voice UDP to the same port number as the control connection \u2014 the lease MUST give the same number for both or voice silently degrades to TCP tunnelling."},
	{Key: "teamspeak3", Label: "TeamSpeak 3", Port: 9987, Protocol: "udp", Record: "srv",
		Service: "ts3", Follows: "srv", Group: "auto",
		Note: "Automatic for the voice port only (the label is _ts3._udp, never _tcp); file transfer 30033 and ServerQuery 10011 are not covered, and players should enter the bare hostname."},
	{Key: "factorio", Label: "Factorio", Port: 34197, Protocol: "udp", Record: "srv",
		Service: "factorio", Follows: "srv", Group: "auto",
		Note: "Publish exactly one SRV \u2014 the client uses the first record and ignores priority/weight; a wildcard CNAME in the zone breaks the lookup, and the Nintendo Switch build has no SRV support."},
	{Key: "xmpp-client", Label: "XMPP (client-to-server)", Port: 5222, Protocol: "tcp", Record: "srv",
		Service: "xmpp-client", Follows: "srv", Group: "auto",
		Note: "Automatic per connection; your TLS cert must match the domain users type, not node.example.com \u2014 but a client with a hand-entered server and port skips the lookup entirely."},
	{Key: "xmpp-server", Label: "XMPP (federation)", Port: 5269, Protocol: "tcp", Record: "srv",
		Service: "xmpp-server", Follows: "srv", Group: "auto",
		Note: "Peers re-resolve on TTL and will NOT fall back to 5269 once an SRV has answered, so keep the TTL at 60s \u2014 a stale port is a hard outage, not a slow one."},
	{Key: "matrix-fed", Label: "Matrix (federation)", Port: 8448, Protocol: "tcp", Record: "srv",
		Service: "matrix-fed", Follows: "srv", Group: "auto",
		Note: "Federation only \u2014 Element and other clients never read it; and a /.well-known/matrix/server file WINS over this record, so delete it or keep it in sync. (_matrix._tcp is the deprecated old name.)"},
	{Key: "sip-udp", Label: "SIP over UDP", Port: 5060, Protocol: "udp", Record: "srv",
		Service: "sip", Follows: "srv", Group: "auto",
		Note: "Any explicit port in the SIP URI \u2014 including a prefilled '5060' in a softphone's port box \u2014 disables the SRV lookup, so hand out a portless sip:user@example.com and clear that field."},
	{Key: "sip-tcp", Label: "SIP over TCP", Port: 5060, Protocol: "tcp", Record: "srv",
		Service: "sip", Follows: "srv", Group: "auto",
		Note: "Same caveats as sip-udp; PJSIP-based stacks (pjsua, Asterisk res_pjsip) only do SRV when a nameserver is configured, otherwise they resolve A-only and ignore the record without an error."},
	{Key: "sips", Label: "SIP over TLS (SIPS)", Port: 5061, Protocol: "tcp", Record: "srv",
		Service: "sips", Follows: "srv", Group: "auto",
		Note: "Cert is checked against the domain in the SIPS URI, not the SRV target; note that signalling follows the rotation but RTP audio ports are negotiated in SDP and need their own forwarding."},
	{Key: "kerberos-udp", Label: "Kerberos KDC (UDP)", Port: 88, Protocol: "udp", Record: "srv",
		Service: "kerberos", Follows: "srv", Group: "auto",
		Note: "On by default in MIT krb5, but any 'kdc =' line in krb5.conf or a URI record suppresses it and the RFC itself warns many clients only ever try port 88 \u2014 do not rely on this for a rotating port."},
	{Key: "kerberos-tcp", Label: "Kerberos KDC (TCP)", Port: 88, Protocol: "tcp", Record: "srv",
		Service: "kerberos", Follows: "srv", Group: "auto",
		Note: "RFC 4120 requires BOTH _kerberos._udp and _kerberos._tcp to be present if either is \u2014 add this alongside kerberos-udp, never on its own."},
	{Key: "caldav", Label: "CalDAV (calendar)", Port: 443, Protocol: "tcp", Record: "srv",
		Service: "caldavs", Follows: "srv-setup", Group: "setup-only",
		Note: "DAVx5 and Thunderbird read this once when the account is added: the first setup finds the port on its own, then every later rotation breaks the account until it is deleted and re-added."},
	{Key: "carddav", Label: "CardDAV (contacts)", Port: 443, Protocol: "tcp", Record: "srv",
		Service: "carddavs", Follows: "srv-setup", Group: "setup-only",
		Note: "Same one-shot behaviour as caldav; publish TXT 'path=/remote.php/dav/' at the SAME owner name as the SRV, at the domain users actually type (usually the apex), and serve HTTPS with a valid cert."},
	{Key: "web", Label: "Website / HTTPS service", Port: 443, Protocol: "tcp", Record: "https",
		Service: "https", Follows: "manual", Group: "browser",
		Note: "Chrome discards an HTTPS record whose port differs and connects to 443 anyway; only Firefox with DoH on honours port=. Put a fixed-port reverse proxy or tunnel in front \u2014 that is the reliable answer."},
	{Key: "web-plain", Label: "HTTP (no TLS)", Port: 80, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "browser",
		Note: "We deliberately publish no HTTPS record here \u2014 its presence tells browsers to switch to https, the opposite of what you asked for. Nothing carries a port for cleartext HTTP."},
	{Key: "api", Label: "API / webhook endpoint", Port: 8080, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "browser",
		Note: "Clients you control can be pointed at the host:port from `fyrwire status`, which makes this far more practical than a browser \u2014 but nothing discovers it for you."},
	{Key: "jellyfin", Label: "Jellyfin / Emby", Port: 8096, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Every client takes a full URL so http://node.example.com:PORT works, but the saved URL never re-checks DNS, and the 7359/udp autodiscovery is a LAN broadcast that cannot cross the VPN."},
	{Key: "plex", Label: "Plex Media Server", Port: 32400, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Mostly redundant: set 'Manually specify public port' to the forwarded port and Plex republishes it to plex.tv itself; clients connect via plex.direct and never see your DNS at all."},
	{Key: "nextcloud", Label: "Nextcloud", Port: 443, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Sync clients store the URL you type and never re-resolve a port; trusted_domains survives a port change, but overwritehost / overwriteport / overwrite.cli.url need updating each rotation."},
	{Key: "immich", Label: "Immich", Port: 2283, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Server Endpoint URL takes host:port; the maintainers explicitly rejected DNS SVCB/HTTPS lookups in the mobile app, so nothing here will ever follow a port automatically."},
	{Key: "navidrome", Label: "Navidrome / Subsonic", Port: 4533, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "The Subsonic API defines no discovery layer at all \u2014 Symfonium, DSub, Feishin and the rest simply store the base URL you type."},
	{Key: "audiobookshelf", Label: "Audiobookshelf", Port: 13378, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "The app's own placeholder is http://host:13378, so a host:port is expected \u2014 but if you enable SSO the endpoint must be https with a valid certificate."},
	{Key: "home-assistant", Label: "Home Assistant", Port: 8123, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Put node.example.com:PORT in the companion app's External URL; it switches to the Internal URL on your home wifi, and its mDNS discovery is multicast-only so it cannot cross the VPN."},
	{Key: "actual-budget", Label: "Actual Budget", Port: 5006, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "The sync-server URL is stored per device, so every phone and desktop needs re-pointing after a rotation."},
	{Key: "paperless-ngx", Label: "Paperless-ngx", Port: 8000, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Browser-first, so no port discovery; third-party mobile apps do take host:port, and PAPERLESS_URL / allowed hosts must list the name you publish."},
	{Key: "vaultwarden", Label: "Vaultwarden", Port: 8000, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Bitwarden clients refuse plain http \u2014 the Server URL must be https://node.example.com:PORT with a valid cert; the port itself is not covered by the certificate, so a rotation does not break TLS."},
	{Key: "gitea-forgejo", Label: "Gitea / Forgejo", Port: 3000, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Weakest fit in the list: a rotating port rewrites every git remote in every clone on every machine, and ROOT_URL/SSH_PORT must change too \u2014 use a fixed-port tunnel for git."},
	{Key: "frigate", Label: "Frigate NVR", Port: 8971, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Needs several ports at once (8971 UI, 8554 RTSP, 8555 tcp+udp WebRTC), so one forwarded port only gets you the web UI; go2rtc's WebRTC candidates need the external address set separately."},
	{Key: "foundry-vtt", Label: "Foundry Virtual Tabletop", Port: 30000, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "The client is a browser, so nothing can follow the port \u2014 front Foundry with a fixed-port reverse proxy and give players a plain https URL."},
	{Key: "syncthing", Label: "Syncthing", Port: 22000, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Largely redundant \u2014 global discovery and relays already track you; only a peer with a pinned tcp://node.example.com:PORT needs editing, and 22000 is TCP and UDP on the same number."},
	{Key: "qbittorrent", Label: "qBittorrent (WebUI)", Port: 8080, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "DNS is the small half here: what matters is pushing the forwarded port into Settings > Connection > Listening Port. This preset is the WebUI, which is a separate port."},
	{Key: "matrix-client", Label: "Matrix (client access)", Port: 443, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "apps",
		Note: "Clients read /.well-known/matrix/client over HTTPS, not DNS \u2014 the port lives inside that file's base_url, so put the client API behind a fixed-port proxy."},
	{Key: "minecraft-bedrock", Label: "Minecraft (Bedrock)", Port: 19132, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "No SRV support \u2014 and worse, a domain that resolves to an SRV record can break Bedrock connections outright; players type the port in the Add Server dialog."},
	{Key: "valheim", Label: "Valheim", Port: 2456, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Needs the query port at game+1 (2456 and 2457), so non-adjacent leased ports make the server invisible in the Steam browser even when direct join works; -crossplay avoids ports entirely."},
	{Key: "ark", Label: "ARK: Survival Evolved / Ascended", Port: 7777, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Evolved also needs gameport+1 (7777/7778) plus 27015 query; Ascended needs only 7777 \u2014 either way the port is typed by hand and Steam favourites go stale."},
	{Key: "rust", Label: "Rust", Port: 28015, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Players use `client.connect host:PORT` in the F1 console; the query port default differs between builds, so set +server.queryport explicitly rather than assuming 28017."},
	{Key: "7dtd", Label: "7 Days to Die", Port: 26900, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Needs a contiguous four-port block (26900-26903) derived from ServerPort \u2014 a single leased port cannot serve this at all."},
	{Key: "palworld", Label: "Palworld", Port: 8211, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Only 8211/udp is needed to play and -queryport is independently settable, so one lease is enough \u2014 but the player retypes the port on every rotation."},
	{Key: "satisfactory", Label: "Satisfactory (dedicated)", Port: 7777, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Incompatible with a rotating lease: the official wiki states the external port must equal the port the server binds, so a redirected port breaks it regardless of DNS."},
	{Key: "project-zomboid", Label: "Project Zomboid", Port: 16261, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Two ports must both be open (16261 and 16262); they are independently configurable, so two unrelated leases work \u2014 the player still retypes both."},
	{Key: "terraria", Label: "Terraria / tModLoader", Port: 7777, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Join via IP takes host and port in separate fields, and the .NET resolver the client uses cannot return SRV records at all."},
	{Key: "source-engine", Label: "Source engine (CS2, TF2, GMod)", Port: 27015, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "games",
		Note: "Game traffic and the A2S query share one port, so a single lease covers everything \u2014 but players must `connect host:PORT` each time and saved favourites and steam:// links go stale."},
	{Key: "ssh", Label: "SSH", Port: 22, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "OpenSSH has no SRV support (SSHFP is unrelated and carries no port) \u2014 use `ssh -p PORT`, or a Host block with Port in ~/.ssh/config that you update when the port moves."},
	{Key: "wireguard", Label: "WireGuard", Port: 51820, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "wg re-resolves the Endpoint hostname, so a changing ADDRESS is followed automatically \u2014 but the port in 'Endpoint = host:port' is literal and must be edited on every peer."},
	{Key: "rdp", Label: "Remote Desktop (RDP)", Port: 3389, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "mstsc and every other RDP client take host:port literally; hand the port out."},
	{Key: "vnc", Label: "VNC", Port: 5900, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "Viewers take host:port, and the 'host:1' shorthand means 5901 \u2014 which makes a rotating five-digit port unusually easy to mistype."},
	{Key: "ldap", Label: "LDAP directory", Port: 389, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "A registered service name with no client discovery: OpenLDAP only tries _ldap._tcp for a hostless URI like ldap:///dc=example,dc=com, and Active Directory uses different names under _msdcs."},
	{Key: "coturn", Label: "STUN / TURN (coturn)", Port: 3478, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "Browsers resolve ICE server URIs with getaddrinfo \u2014 A/AAAA only \u2014 so stun:/turn: URIs must carry the port literally, and TURN allocates relay ports beyond the listener anyway."},
	{Key: "ftp", Label: "FTP", Port: 21, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "remote",
		Note: "Passive mode negotiates a second data port in-band that no DNS record can express \u2014 use SFTP over the SSH mapping instead."},
	{Key: "imaps", Label: "IMAP over TLS", Port: 993, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "mail",
		Note: "RFC 6186 defines _imaps._tcp, but Thunderbird and Outlook do not implement it (they use their own autoconfig), and where it exists it is setup-time only."},
	{Key: "submission", Label: "SMTP submission", Port: 587, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "mail",
		Note: "Same as imaps \u2014 and note inbound mail is routed by MX, which has no port field at all, so a rotating port for receiving mail can never be made to work."},
	{Key: "generic-tcp", Label: "Any TCP service", Port: 0, Protocol: "tcp", Record: "none",
		Service: "", Follows: "manual", Group: "generic",
		Note: "No port discovery \u2014 FyrWire keeps the A record correct and `fyrwire status` prints the current host:port."},
	{Key: "generic-udp", Label: "Any UDP service", Port: 0, Protocol: "udp", Record: "none",
		Service: "", Follows: "manual", Group: "generic",
		Note: "No port discovery \u2014 FyrWire keeps the A record correct and `fyrwire status` prints the current host:port."},
	{Key: "custom-srv", Label: "Custom SRV service (advanced)", Port: 0, Protocol: "tcp", Record: "srv",
		Service: "", Follows: "srv", Group: "generic",
		Note: "Only pick this if you have CHECKED that your client really resolves SRV \u2014 see the checklist `fyrwire presets --custom`; guessing wrong sends people to the old port with no error."},
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

var FollowsHelp = map[string]string{
	"srv":       "the client follows the SRV record — fully automatic",
	"srv-setup": "read ONCE when the server is added; a rotation breaks it",
	"manual":    "nothing reads the SRV; hand out host:port yourself",
}

func ValidateService(name string) error {
	if name == "" {
		return errf("service label is empty")
	}
	if len(name) > 15 {
		return errf("service label %q is %d characters; RFC 6335 allows 15",
			name, len(name))
	}
	hasLetter := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			hasLetter = true
		case c >= '0' && c <= '9':
		case c == '-':
			if i == 0 || i == len(name)-1 {
				return errf("service label %q may not start or end with a hyphen",
					name)
			}
			if name[i-1] == '-' {
				return errf("service label %q has two hyphens in a row", name)
			}
		default:
			return errf("service label %q contains %q; only letters, digits and "+
				"hyphens are allowed", name, string(c))
		}
	}
	if !hasLetter {
		return errf("service label %q must contain at least one letter", name)
	}
	return nil
}

func errf(format string, a ...any) error {
	return &serviceError{msg: sprintf(format, a...)}
}

type serviceError struct{ msg string }

func (e *serviceError) Error() string { return e.msg }

func init() {
	registerLookup(func(key string) (string, bool) {
		p, ok := byKey[key]
		if !ok {
			return "", false
		}
		return p.Record, true
	})
}
