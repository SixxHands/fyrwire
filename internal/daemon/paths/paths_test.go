package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxUsesXdgLayout(t *testing.T) {
	defer SetGoosForTest("linux")()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("FYRWIRE_CONFIG", "")
	t.Setenv("FYRWIRE_STATE", "")
	if !strings.HasSuffix(filepath.ToSlash(ConfigFile()), "/.config/fyrwire/fyrwire.toml") {
		t.Errorf("config = %q", ConfigFile())
	}
	if !strings.HasSuffix(filepath.ToSlash(StateFile()), "/.local/state/fyrwire/state.json") {
		t.Errorf("state = %q", StateFile())
	}
}

func TestMacUsesApplicationSupport(t *testing.T) {
	defer SetGoosForTest("darwin")()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("FYRWIRE_CONFIG", "")
	if !strings.Contains(filepath.ToSlash(ConfigFile()), "Library/Application Support/FyrWire") {
		t.Errorf("config = %q", ConfigFile())
	}
}

func TestWindowsConfigRoamsAndStateStaysLocal(t *testing.T) {
	defer SetGoosForTest("windows")()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("FYRWIRE_CONFIG", "")
	t.Setenv("FYRWIRE_STATE", "")
	t.Setenv("APPDATA", `C:\U\AppData\Roaming`)
	t.Setenv("LOCALAPPDATA", `C:\U\AppData\Local`)
	if !strings.Contains(ConfigFile(), "Roaming") {
		t.Errorf("config should roam: %q", ConfigFile())
	}
	if strings.Contains(StateFile(), "Roaming") {
		t.Errorf("state must not roam: %q", StateFile())
	}
	if !strings.Contains(StateFile(), "Local") {
		t.Errorf("state = %q", StateFile())
	}
}

func TestExplicitXdgWinsOnEveryPlatform(t *testing.T) {
	for _, sys := range []string{"linux", "darwin", "windows"} {
		t.Run(sys, func(t *testing.T) {
			defer SetGoosForTest(sys)()
			t.Setenv("XDG_CONFIG_HOME", "/custom")
			t.Setenv("FYRWIRE_CONFIG", "")
			want := "/custom/fyrwire/fyrwire.toml"
			if got := filepath.ToSlash(ConfigFile()); got != want {
				t.Errorf("got %q want %q", got, want)
			}
		})
	}
}

func TestExplicitEnvOverrideBeatsEverything(t *testing.T) {
	defer SetGoosForTest("linux")()
	t.Setenv("FYRWIRE_CONFIG", "/exact/path.toml")
	if got := ConfigFile(); got != "/exact/path.toml" {
		t.Errorf("got %q", got)
	}
}

func TestLegacyLocationsOnlyWhenLayoutDiffers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("FYRWIRE_CONFIG", "")
	t.Setenv("FYRWIRE_STATE", "")
	restore := SetGoosForTest("linux")
	if got := LegacyLocations(); len(got) != 0 {
		t.Errorf("linux layout unchanged, got: %v", got)
	}
	restore()
	defer SetGoosForTest("darwin")()
	if got := LegacyLocations(); len(got) == 0 {
		t.Error("expected legacy locations on macOS")
	}
}
