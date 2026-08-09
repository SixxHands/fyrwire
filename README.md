# FyrWire

*Keep your connections... connected!*

FyrWire watches a rotating VPN or router port forward (NAT-PMP, UPnP, or a custom command), updates your DNS `A` and `SRV` records automatically, and optionally relays the forwarded port straight to your local service.

Static Go binary with no runtime dependencies.

## What it does

- **Port discovery:** Speaks NAT-PMP directly over UDP (RFC 6886), supports UPnP via `upnpc`, or reads `host:port` from any shell command.
- **DNS updates:** Updates one `A` record for the machine plus per-service `SRV` or `HTTPS` records on **Cloudflare** or **deSEC**, publishing only when something changes.
- **Built-in TCP/UDP relay:** Forwards traffic from the tunnel interface's granted port to your local service (`127.0.0.1:<port>`) without needing `socat` or shell loops. Only rebinds when a port actually changes.
- **Alerts:** Sends transition alerts (outage and recovery) to Discord, Slack, generic webhooks, or a shell command (or so I think I haven't fully tested discord or slack yet myself goodluck).
- **Service installer:** Generates a per-user `systemd` unit on Linux, `launchd` plist on macOS, or logon scheduled task on Windows.

## Building

Requires Go 1.25+.

```sh
go build -o dist/fyrwire ./cmd/fyrwire
```

Or with Make:

```sh
make build      # local static binary (CGO_ENABLED=0)
make test       # run test suite
make release    # cross-compile linux/darwin/windows/freebsd binaries
```

## Quick start

Run setup (it checks your NAT-PMP gateway, verifies your DNS token, and writes `fyrwire.toml`):

```sh
fyrwire setup
export FYRWIRE_CF_TOKEN="your-token"   # or FYRWIRE_DESEC_TOKEN
fyrwire once
fyrwire install
```

Write a starter config and edit it by hand:

```sh
fyrwire init
fyrwire doctor
fyrwire run
```

## Commands

| Command | What it does |
|---|---|
| `fyrwire setup [--force]` | Guided first-run setup |
| `fyrwire init [--force]` | Write a starter `fyrwire.toml` |
| `fyrwire add` | Append a service to `fyrwire.toml` |
| `fyrwire remove NAME [--keep-dns] [--yes]` | Remove a service and its DNS record |
| `fyrwire once` | Run one update cycle and exit |
| `fyrwire run` | Run the update loop until stopped |
| `fyrwire doctor` | Check config, platform info, and listening ports |
| `fyrwire check` | Resolve published SRV records and test TCP connections |
| `fyrwire status` | Show last published address, ports, and error count |
| `fyrwire presets` | List built-in presets and client SRV behavior |
| `fyrwire sources` | List available sources and DNS providers |
| `fyrwire install` / `uninstall` | Install or remove the user background service |
| `fyrwire version` | Print version and banner |

Global flags: `--config PATH`, `--state PATH`.

## Example config

```toml
[fyrwire]
version = 1

[source]
type     = "natpmp"
gateway  = "10.2.0.1"
lifetime = 120

[provider]
type      = "cloudflare"
api_token = "env:FYRWIRE_CF_TOKEN"
zone_id   = "env:FYRWIRE_CF_ZONE"

[node]
a_name = "node.example.com"
ttl    = 60
relay  = true
# relay_udp = true

[[service]]
name          = "minecraft"
internal_port = 25565
srv_name      = "_minecraft._tcp.play.example.com"

[agent]
interval = 45
```

Secrets written as `"env:VAR_NAME"` are read from the environment so `fyrwire.toml` does not need raw tokens on disk.

DNS records published for that config look like:

```text
A    node.example.com                    -> 203.0.113.49
SRV  _minecraft._tcp.play.example.com    -> 0 5 51471 node.example.com
```

### Multiple services and single-port VPNs

Each `[[service]]` shares the node's single `A` record and gets its own `SRV` record. If a gateway (such as ProtonVPN) assigns only one forwarded port per session instead of per-service mappings, FyrWire detects it via internal port 0, uses the real assigned port, and logs a warning if multiple services share a protocol.

### UDP relay notes

TCP relaying is enabled with `relay = true`. UDP relaying is opt-in (`relay_udp = true`) because:

1. Any unprivileged userspace UDP relay forwards backend replies to the packet's source address, so rate limits and session caps (`[node.udp]`) are enabled by default.
2. The backend sees relayed traffic as coming from `127.0.0.1` (preserving client IPs requires root / TPROXY).

## Other Notes

- Do not run FyrWire alongside another NAT-PMP client on the same gateway and internal port; the gateway will allocate a second port that does not match DNS.
- If discovery fails (e.g. tunnel blip), FyrWire logs the error and leaves the last known good DNS records in place instead of deleting them.

## License

GPL-3.0. See [LICENSE](LICENSE).
