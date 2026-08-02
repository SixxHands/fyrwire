package setup

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sixxhands/fyrwire/internal/config"
	"github.com/sixxhands/fyrwire/internal/model"
	"github.com/sixxhands/fyrwire/internal/preset"
	"github.com/sixxhands/fyrwire/internal/provider"
)

const Starter = `# FyrWire — keep names pointed at a moving endpoint.
#
# Secrets: any value written as "env:NAME" is read from that environment
# variable instead, so this file stays safe to commit.

[fyrwire]
version = 1

[source]
# Where the current address and ports come from.
type     = "natpmp"
gateway  = "10.2.0.1"
lifetime = 120

[provider]
# Where the names live.
type      = "cloudflare"
api_token = "env:FYRWIRE_CF_TOKEN"
zone_id   = "env:FYRWIRE_CF_ZONE"

[node]
# ONE A record for the machine. Every service points its SRV at this name, so
# the address is published once and cannot disagree with itself.
a_name = "node.example.com"
ttl    = 60

# Bind the granted port locally and forward it to the service. Without this,
# DNS sends clients to the right address and port and nothing is listening.
# relay     = true
#
# UDP relaying is OPT-IN, and deliberately so. Any UDP relay forwards the
# backend's reply to a source address that is trivially spoofable, which makes
# it an open reflector by construction. FyrWire caps sessions, caps sessions
# per source IP and rate-limits new sessions, but those are mitigations, not a
# fix. Note too that the backend will see every client as 127.0.0.1: preserving
# real source IPs needs root, so server-side IP bans, per-IP limits and
# client-IP logs stop meaning anything behind the relay.
# relay_udp = false

# [node.udp]
# idle_new         = 30    # seconds before an unanswered session is dropped
# idle_established = 180   # seconds once the backend has replied
# max_sessions     = 512
# max_per_ip       = 8
# new_per_sec      = 32

# One [[service]] per thing you expose. Each gets its own forwarded port and
# its own SRV record; they share the A record above.
[[service]]
name          = "minecraft"
internal_port = 25565
srv_name      = "_minecraft._tcp.play.example.com"

[agent]
interval = 45
`

func Init(configPath string, force bool) error {
	if _, err := os.Stat(configPath); err == nil && !force {
		return model.Errorf("%s already exists — pass --force to overwrite", configPath)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(configPath, []byte(Starter), 0o644); err != nil {
		return err
	}
	good("wrote %s", configPath)
	say("  %sedit it, then run `fyrwire doctor`%s", dim, reset)
	return nil
}

func Add(configPath string) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return model.Errorf("cannot read %s: %v", configPath, err)
	}
	cfg, err := config.Parse(string(raw))
	if err != nil {
		return err
	}
	zone := zoneOf(cfg)

	key := ask("Preset (or `list`, or `custom`)", "minecraft")
	for key == "list" {
		printPresets()
		key = ask("Preset (or `custom`)", "minecraft")
	}
	p, found := preset.Get(key)
	var c serviceChoice
	if key == "custom" || !found {
		if !found && key != "custom" {
			fail("no preset called %q", key)
			return nil
		}
		c = askCustom(zone)
		if c.name == "" {
			return model.Errorf("nothing added")
		}
	} else {
		c = serviceChoice{name: p.Key, presetKey: p.Key, protocol: p.Protocol,
			record: p.Record, internalPort: p.Port}
		c.internalPort = askPort(p, nil)
		label := ask("Subdomain players will use", strings.Split(zone, ".")[0])
		c.srvName = fmt.Sprintf("_%s._%s.%s.%s", p.Service, p.Protocol, label, zone)
		if p.Follows != "srv" && p.Note != "" {
			note("%s", p.Note)
		}
	}
	for _, existing := range cfg.Service {
		if existing.Name == c.name {
			return model.Errorf(
				"a service called %q is already in this config — pick another "+
					"name, since state and relays are keyed on it", c.name)
		}
	}

	block := fmt.Sprintf("\n[[service]]\nname          = %q\n", c.name)
	if c.presetKey != "" {
		block += fmt.Sprintf("preset        = %q\n", c.presetKey)
	}
	block += fmt.Sprintf("internal_port = %d\nprotocol      = %q\n"+
		"record        = %q\nsrv_name      = %q\n",
		c.internalPort, c.protocol, c.record, c.srvName)

	text := string(raw)
	if i := strings.Index(text, "\n[agent]"); i >= 0 {
		text = text[:i] + "\n" + block + text[i:]
	} else {
		text = strings.TrimRight(text, "\n") + "\n" + block
	}
	if _, err := config.Parse(text); err != nil {

		return model.Errorf("refusing to write: the result would be invalid (%v)", err)
	}
	if err := os.WriteFile(configPath, []byte(text), 0o644); err != nil {
		return err
	}
	good("added %s to %s", c.name, configPath)
	say("  %sit will be published on the next cycle%s", dim, reset)
	return nil
}

func Remove(configPath, name string, keepDNS, assumeYes bool) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return model.Errorf("cannot read %s: %v", configPath, err)
	}
	cfg, err := config.Parse(string(raw))
	if err != nil {
		return err
	}
	var target *config.Service
	var names []string
	for i := range cfg.Service {
		names = append(names, cfg.Service[i].Name)
		if cfg.Service[i].Name == name {
			target = &cfg.Service[i]
		}
	}
	if target == nil {
		return model.Errorf("no service called %q — have: %s", name,
			strings.Join(names, ", "))
	}

	if len(cfg.Service) == 1 {
		return model.Errorf(
			"%q is the only service in %s, and a config with none cannot load.\n"+
				"  Add another service first, or delete the config file if you "+
				"are done with this node — the DNS records it published stay "+
				"until you remove them.",
			name, configPath)
	}

	if !keepDNS && !assumeYes {
		rtype := "SRV"
		if target.Record == "https" {
			rtype = "HTTPS"
		}
		note("this will DELETE the DNS record %s %s", rtype, target.SrvName)
		if !yes("Remove it?", false) {
			say("  %scancelled%s", dim, reset)
			return nil
		}
	}

	if !keepDNS {
		prov, perr := provider.Build(cfg.Provider)
		if perr != nil {
			note("cannot reach the DNS provider (%v)", perr)
			note("removing from the config only; the record will be left behind")
		} else {
			rtype := "SRV"
			if target.Record == "https" {
				rtype = "HTTPS"
			}
			if target.Record == "none" {
				rtype = ""
			}
			if rtype != "" {

				what, derr := prov.Delete(rtype, target.SrvName, cfg.Node.AName)
				switch {
				case derr != nil:
					fail("%v", derr)
					note("leaving the record in place and removing it from the config")
				case what == "":
					say("  %sno %s record at %s to remove%s", dim, rtype,
						target.SrvName, reset)
				default:
					good("%s", what)
				}
			}
		}
	}

	text := removeServiceBlock(string(raw), name)
	newCfg, err := config.Parse(text)
	if err != nil {
		return model.Errorf("refusing to write: the result would be invalid (%v)", err)
	}
	if len(newCfg.Service) != len(cfg.Service)-1 {
		return model.Errorf(
			"could not cleanly remove %q from the file — edit it by hand", name)
	}
	if err := os.WriteFile(configPath, []byte(text), 0o644); err != nil {
		return err
	}
	good("removed %s from %s", name, configPath)
	say("  %sthe relay for it stops on the next reconcile%s", dim, reset)
	return nil
}

var serviceHeader = regexp.MustCompile(`(?m)^\[\[service\]\]\s*$`)

func removeServiceBlock(text, name string) string {
	starts := serviceHeader.FindAllStringIndex(text, -1)
	for i, s := range starts {
		end := len(text)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		if next := nextSectionAfter(text, s[1]); next >= 0 && next < end {
			end = next
		}
		block := text[s[0]:end]
		if regexp.MustCompile(`(?m)^\s*name\s*=\s*"` +
			regexp.QuoteMeta(name) + `"\s*$`).MatchString(block) {
			return text[:s[0]] + text[end:]
		}
	}
	return text
}

var anySection = regexp.MustCompile(`(?m)^\[`)

func nextSectionAfter(text string, from int) int {
	loc := anySection.FindStringIndex(text[from:])
	if loc == nil {
		return -1
	}
	return from + loc[0]
}

func zoneOf(cfg *config.Config) string {
	if d, err := cfg.Provider.String("domain"); err == nil && d != "" {
		return d
	}

	parts := strings.Split(cfg.Node.AName, ".")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], ".")
	}
	return cfg.Node.AName
}

func Check(configPath string, timeout time.Duration) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	plan, err := cfg.Plan()
	if err != nil {
		return err
	}
	allOK := true
	for _, svc := range plan.Services {
		say("  %s%s%s", text, svc.Key, reset)
		if svc.Record != "srv" {
			say("    %spublished as %s, which this check cannot resolve%s",
				dim, svc.Record, reset)
			continue
		}

		parts := strings.SplitN(strings.TrimPrefix(svc.SrvName, "_"), ".", 3)
		if len(parts) != 3 {
			fail("%s does not look like an SRV name", svc.SrvName)
			allOK = false
			continue
		}
		service := parts[0]
		proto := strings.TrimPrefix(parts[1], "_")
		_, addrs, err := net.LookupSRV(service, proto, parts[2])
		if err != nil || len(addrs) == 0 {
			fail("SRV %s did not resolve (%v)", svc.SrvName, err)
			allOK = false
			continue
		}
		for _, a := range addrs {
			host := strings.TrimSuffix(a.Target, ".")
			good("SRV -> %s:%d", host, a.Port)
			ips, err := net.LookupHost(host)
			if err != nil || len(ips) == 0 {
				fail("  %s has no address — the SRV points at a name with no A record", host)
				allOK = false
				continue
			}
			for _, ip := range ips {
				if svc.Protocol != "tcp" {

					say("    %sA -> %s  (UDP: reachability is not provable "+
						"without a protocol-aware probe)%s", dim, ip, reset)
					continue
				}
				conn, err := net.DialTimeout("tcp",
					net.JoinHostPort(ip, strconv.Itoa(int(a.Port))), timeout)
				if err != nil {
					fail("  TCP %s:%d UNREACHABLE — %v", ip, a.Port, err)
					allOK = false
					continue
				}
				conn.Close()

				good("  TCP %s:%d accepts connections", ip, a.Port)
			}
		}
	}
	if !allOK {
		return model.Errorf("one or more checks failed")
	}
	return nil
}
