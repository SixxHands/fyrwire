package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/sixxhands/fyrwire/internal/network/provider"
	_ "github.com/sixxhands/fyrwire/internal/network/source"
	"github.com/sixxhands/fyrwire/internal/node/config"
)

const sample = `# my notes
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

# minecraft note
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

func Test_remove_service_preserves_rest_of_file(t *testing.T) {
	got := removeServiceBlock(sample, "valheim")
	if strings.Contains(got, "valheim") {
		t.Error("service is still present")
	}
	for _, must := range []string{
		"# my notes", "# minecraft note", `gateway = "10.2.0.1"`, "interval = 45", `name = "minecraft"`,
	} {
		if !strings.Contains(got, must) {
			t.Errorf("lost %q", must)
		}
	}
}

func Test_removed_result_still_parses(t *testing.T) {
	got := removeServiceBlock(sample, "minecraft")
	loaded, err := config.ReadText(got)
	if err != nil {
		t.Fatalf("invalid config after removal: %v", err)
	}
	if len(loaded.Service) != 1 || loaded.Service[0].Name != "valheim" {
		t.Errorf("services = %+v", loaded.Service)
	}
}

func Test_removing_absent_service_changes_nothing(t *testing.T) {
	if got := removeServiceBlock(sample, "nope"); got != sample {
		t.Error("unmatched name must leave file untouched")
	}
}

func Test_starter_config_is_valid(t *testing.T) {
	t.Setenv("FYRWIRE_CF_TOKEN", "t")
	t.Setenv("FYRWIRE_CF_ZONE", "z")
	if _, err := config.ReadText(Starter); err != nil {
		t.Fatalf("starter config is invalid: %v", err)
	}
}

func Test_zone_derived_from_provider(t *testing.T) {
	loaded, err := config.ReadText(sample)
	if err != nil {
		t.Fatal(err)
	}
	if got := zoneOf(loaded); got != "example.com" {
		t.Errorf("desec: got %q", got)
	}
	cf := strings.Replace(sample,
		"type = \"desec\"\ntoken = \"env:T\"\ndomain = \"example.com\"",
		"type = \"cloudflare\"\napi_token = \"env:T\"\nzone_id = \"z\"", 1)
	loaded2, err := config.ReadText(cf)
	if err != nil {
		t.Fatal(err)
	}
	if got := zoneOf(loaded2); got != "example.com" {
		t.Errorf("cloudflare: got %q", got)
	}
}

func Test_removing_only_service_is_refused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "one.toml")
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
		t.Fatal("expected refusal")
	}
	if !strings.Contains(err.Error(), "only service") {
		t.Errorf("unexpected error: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != one {
		t.Error("refused removal must not modify config")
	}
}
