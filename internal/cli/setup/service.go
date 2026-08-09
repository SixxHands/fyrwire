package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sixxhands/fyrwire/internal/network/provider"
	_ "github.com/sixxhands/fyrwire/internal/network/provider/backends"
	_ "github.com/sixxhands/fyrwire/internal/network/source/gateway"
	_ "github.com/sixxhands/fyrwire/internal/network/source/manual"
	"github.com/sixxhands/fyrwire/internal/node/config"
	"github.com/sixxhands/fyrwire/internal/node/model"
	"github.com/sixxhands/fyrwire/internal/node/preset"
)

const Starter = `# FyrWire config
# Use "env:NAME" for secrets so this file can be kept safely.

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

# Forward the granted port to the local service:
# relay     = true
# relay_udp = false

# [node.udp]
# idle_new         = 30
# idle_established = 180
# max_sessions     = 512
# max_per_ip       = 8
# new_per_sec      = 32

[[service]]
name          = "minecraft"
internal_port = 25565
srv_name      = "_minecraft._tcp.play.example.com"

[agent]
interval = 45
`

func Init(configPath string, force bool) error {
	if _, err := os.Stat(configPath); err == nil && !force {
		return model.Errorf("%s already exists; pass --force to overwrite", configPath)
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
	loaded, err := config.ReadText(string(raw))
	if err != nil {
		return err
	}
	c, err := readAddedService(zoneOf(loaded))
	if err != nil {
		return err
	}
	for _, existing := range loaded.Service {
		if existing.Name == c.name {
			return model.Errorf("service %q already exists in this config", c.name)
		}
	}
	updated := insertServiceBlock(string(raw), formatServiceBlock(c))
	if _, err := config.ReadText(updated); err != nil {
		return model.Errorf("refusing to write invalid config: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(updated), 0o644); err != nil {
		return err
	}
	good("added %s to %s", c.name, configPath)
	return nil
}

func readAddedService(zone string) (serviceChoice, error) {
	key := ask("Preset (or `list`, or `custom`)", "minecraft")
	for key == "list" {
		printPresets()
		key = ask("Preset (or `custom`)", "minecraft")
	}
	if key == "custom" {
		c := askCustom(zone)
		if c.name == "" {
			return serviceChoice{}, model.Errorf("nothing added")
		}
		return c, nil
	}
	p, found := preset.Get(key)
	if !found {
		return serviceChoice{}, model.Errorf("no preset called %q", key)
	}
	c := serviceChoice{
		name: p.Key, presetKey: p.Key, protocol: p.Protocol,
		record: p.Record, internalPort: askPort(p, nil),
	}
	label := ask("Subdomain players will use", strings.Split(zone, ".")[0])
	c.srvName = fmt.Sprintf("_%s._%s.%s.%s", p.Service, p.Protocol, label, zone)
	if p.Follows != "srv" && p.Note != "" {
		note("%s", p.Note)
	}
	return c, nil
}

func formatServiceBlock(c serviceChoice) string {
	block := fmt.Sprintf("\n[[service]]\nname          = %q\n", c.name)
	if c.presetKey != "" {
		block += fmt.Sprintf("preset        = %q\n", c.presetKey)
	}
	block += fmt.Sprintf("internal_port = %d\nprotocol      = %q\nrecord        = %q\nsrv_name      = %q\n",
		c.internalPort, c.protocol, c.record, c.srvName)
	return block
}

func insertServiceBlock(text, block string) string {
	if i := strings.Index(text, "\n[agent]"); i >= 0 {
		return text[:i] + "\n" + block + text[i:]
	}
	return strings.TrimRight(text, "\n") + "\n" + block
}

func Remove(configPath, name string, keepDns, assumeYes bool) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return model.Errorf("cannot read %s: %v", configPath, err)
	}
	loaded, err := config.ReadText(string(raw))
	if err != nil {
		return err
	}
	target, err := findTargetService(loaded, name, configPath)
	if err != nil {
		return err
	}
	if !keepDns {
		canceled, err := removeRemoteRecord(loaded, target, assumeYes)
		if err != nil || canceled {
			return err
		}
	}
	return writeAfterRemove(configPath, string(raw), name, len(loaded.Service)-1)
}

func findTargetService(loaded *config.ConfigData, name, configPath string) (*config.ServiceConfigData, error) {
	var target *config.ServiceConfigData
	var names []string
	for i := range loaded.Service {
		names = append(names, loaded.Service[i].Name)
		if loaded.Service[i].Name == name {
			target = &loaded.Service[i]
		}
	}
	if target == nil {
		return nil, model.Errorf("no service called %q (have: %s)", name, strings.Join(names, ", "))
	}
	if len(loaded.Service) == 1 {
		return nil, model.Errorf(
			"%q is the only service in %s; add another service first or remove the config file",
			name, configPath)
	}
	return target, nil
}

func recordTypeFor(record string) string {
	switch record {
	case "https":
		return "HTTPS"
	case "none":
		return ""
	default:
		return "SRV"
	}
}

func removeRemoteRecord(loaded *config.ConfigData, target *config.ServiceConfigData, assumeYes bool) (bool, error) {
	recordType := recordTypeFor(target.Record)
	if recordType == "" {
		return false, nil
	}
	if !assumeYes {
		note("this will remove DNS record %s %s", recordType, target.SrvName)
		if !askYesNo("Remove it?", false) {
			say("  %scanceled%s", dim, reset)
			return true, nil
		}
	}
	providerBlueprint, err := provider.CreateProviderBlueprint(loaded.Provider)
	if err != nil {
		note("cannot reach DNS provider (%v); removing from config only", err)
		return false, nil
	}
	prov, err := provider.ProviderFactory{}.Create(providerBlueprint)
	if err != nil {
		note("cannot reach DNS provider (%v); removing from config only", err)
		return false, nil
	}
	what, err := prov.Remove(recordType, target.SrvName, loaded.Node.AName)
	switch {
	case err != nil:
		fail("%v", err)
		note("leaving the DNS record in place and removing from config")
	case what == "":
		say("  %sno %s record at %s to remove%s", dim, recordType, target.SrvName, reset)
	default:
		good("%s", what)
	}
	return false, nil
}

func writeAfterRemove(configPath, rawText, name string, wantCount int) error {
	updated := removeServiceBlock(rawText, name)
	nextConfig, err := config.ReadText(updated)
	if err != nil {
		return model.Errorf("refusing to write invalid config: %v", err)
	}
	if len(nextConfig.Service) != wantCount {
		return model.Errorf("could not cleanly remove %q; edit %s by hand", name, configPath)
	}
	if err := os.WriteFile(configPath, []byte(updated), 0o644); err != nil {
		return err
	}
	good("removed %s from %s", name, configPath)
	return nil
}

var (
	serviceHeader = regexp.MustCompile(`(?m)^\[\[service\]\]\s*$`)
	anySection    = regexp.MustCompile(`(?m)^\[`)
)

func removeServiceBlock(text, name string) string {
	starts := serviceHeader.FindAllStringIndex(text, -1)
	nameMatch := regexp.MustCompile(`(?m)^\s*name\s*=\s*"` + regexp.QuoteMeta(name) + `"\s*$`)
	for i, s := range starts {
		end := len(text)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		if next := nextSectionAfter(text, s[1]); next >= 0 && next < end {
			end = next
		}
		if nameMatch.MatchString(text[s[0]:end]) {
			return text[:s[0]] + text[end:]
		}
	}
	return text
}

func nextSectionAfter(text string, from int) int {
	loc := anySection.FindStringIndex(text[from:])
	if loc == nil {
		return -1
	}
	return from + loc[0]
}

func zoneOf(loaded *config.ConfigData) string {
	if d, err := loaded.Provider.String("domain"); err == nil && d != "" {
		return d
	}
	parts := strings.Split(loaded.Node.AName, ".")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], ".")
	}
	return loaded.Node.AName
}
