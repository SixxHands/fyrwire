package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sixxhands/fyrwire/internal/config"
	_ "github.com/sixxhands/fyrwire/internal/provider"
	_ "github.com/sixxhands/fyrwire/internal/source"
)

const sample = `# my notes, which must survive
[fyrwire]
version = 1

[source]
type = "natpmp"
gateway = "10.2.0.1"

[provider]
type = "desec"
token = "env:T"
domain = "example.com"

[node]
a_name = "node.example.com"

# this comment belongs to minecraft
[[service]]
name = "minecraft"
internal_port = 25565
srv_name = "_minecraft._tcp.play.example.com"

[[service]]
name = "valheim"
internal_port = 2456
protocol = "udp"
srv_name = "_valheim._udp.vh.example.com"

[agent]
interval = 45
`

func TestRemovingAServiceLeavesEverythingElseByteIdentical(t *testing.T) {
	got := removeServiceBlock(sample, "valheim")
	if strings.Contains(got, "valheim") {
		t.Error("the service is still present")
	}
	for _, must := range []string{
		"# my notes, which must survive",
		"# this comment belongs to minecraft",
		`gateway = "10.2.0.1"`,
		"interval = 45",
		`name = "minecraft"`,
	} {
		if !strings.Contains(got, must) {
			t.Errorf("lost %q", must)
		}
	}
}

func TestTheResultStillParses(t *testing.T) {
	got := removeServiceBlock(sample, "minecraft")
	cfg, err := config.Parse(got)
	if err != nil {
		t.Fatalf("removal produced an invalid config: %v", err)
	}
	if len(cfg.Service) != 1 || cfg.Service[0].Name != "valheim" {
		t.Errorf("services = %+v", cfg.Service)
	}
}

func TestRemovingTheFirstServiceWorksToo(t *testing.T) {

	got := removeServiceBlock(sample, "minecraft")
	if strings.Contains(got, `name = "minecraft"`) {
		t.Error("first service not removed")
	}
	if !strings.Contains(got, `name = "valheim"`) {
		t.Error("removed the wrong one")
	}
}

func TestRemovingAnAbsentServiceChangesNothing(t *testing.T) {
	if got := removeServiceBlock(sample, "nope"); got != sample {
		t.Error("an unmatched name must leave the file untouched")
	}
}

func TestStarterConfigPassesItsOwnValidator(t *testing.T) {
	t.Setenv("FYRWIRE_CF_TOKEN", "t")
	t.Setenv("FYRWIRE_CF_ZONE", "z")
	if _, err := config.Parse(Starter); err != nil {
		t.Fatalf("the starter config is invalid: %v", err)
	}
}

func TestZoneIsDerivedFromEitherProviderShape(t *testing.T) {
	cfg, err := config.Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if got := zoneOf(cfg); got != "example.com" {
		t.Errorf("desec: got %q", got)
	}

	cf := strings.Replace(sample,
		"type = \"desec\"\ntoken = \"env:T\"\ndomain = \"example.com\"",
		"type = \"cloudflare\"\napi_token = \"env:T\"\nzone_id = \"z\"", 1)
	cfg2, err := config.Parse(cf)
	if err != nil {
		t.Fatal(err)
	}
	if got := zoneOf(cfg2); got != "example.com" {
		t.Errorf("cloudflare: got %q", got)
	}
}

func TestRemovingTheOnlyServiceExplainsItself(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "one.toml")
	one := `[source]
type = "static"
host = "1.2.3.4"

[provider]
type = "cloudflare"
api_token = "t"
zone_id = "z"

[node]
a_name = "node.example.com"

[[service]]
name = "only"
internal_port = 25565
srv_name = "_mc._tcp.example.com"
`
	if err := os.WriteFile(path, []byte(one), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Remove(path, "only", true, true)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, "only service") {
		t.Errorf("must name the actual problem: %v", err)
	}

	for _, invented := range []string{"--keep-config", "--force-remove"} {
		if strings.Contains(msg, invented) {
			t.Errorf("message references a flag that does not exist: %q", invented)
		}
	}

	after, _ := os.ReadFile(path)
	if string(after) != one {
		t.Error("a refused removal must not modify the config")
	}
}

func TestAdvertisedRemoveFlagsExist(t *testing.T) {

	var _ = func(p, n string) error { return Remove(p, n, true, true) }
}
