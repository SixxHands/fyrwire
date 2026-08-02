package setup

import (
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
