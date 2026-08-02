package paths

import (
	"strings"
	"testing"
)

func TestLinuxUsesXDGLayout(t *testing.T) {
	defer SetGOOSForTest("linux")()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("FYRWIRE_CONFIG", "")
	t.Setenv("FYRWIRE_STATE", "")
	if !strings.HasSuffix(ConfigFile(), "/.config/fyrwire/fyrwire.toml") {
		t.Errorf("config = %q", ConfigFile())
	}
	if !strings.HasSuffix(StateFile(), "/.local/state/fyrwire/state.json") {
		t.Errorf("state = %q", StateFile())
	}
}

func TestMacUsesApplicationSupport(t *testing.T) {
	defer SetGOOSForTest("darwin")()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("FYRWIRE_CONFIG", "")
	if !strings.Contains(ConfigFile(), "Library/Application Support/FyrWire") {
		t.Errorf("config = %q", ConfigFile())
	}
}

func TestWindowsConfigRoamsButStateDoesNot(t *testing.T) {
	defer SetGOOSForTest("windows")()
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
		t.Errorf("state must NOT roam: %q", StateFile())
	}
	if !strings.Contains(StateFile(), "Local") {
		t.Errorf("state = %q", StateFile())
	}
}

func TestExplicitXDGWinsOnEveryPlatform(t *testing.T) {
	for _, sys := range []string{"linux", "darwin", "windows"} {
		t.Run(sys, func(t *testing.T) {
			defer SetGOOSForTest(sys)()
			t.Setenv("XDG_CONFIG_HOME", "/custom")
			t.Setenv("FYRWIRE_CONFIG", "")
			want := "/custom/fyrwire/fyrwire.toml"
			if got := ConfigFile(); got != want {
				t.Errorf("got %q want %q", got, want)
			}
		})
	}
}

func TestExplicitEnvOverrideBeatsEverything(t *testing.T) {
	defer SetGOOSForTest("linux")()
	t.Setenv("FYRWIRE_CONFIG", "/exact/path.toml")
	if got := ConfigFile(); got != "/exact/path.toml" {
		t.Errorf("got %q", got)
	}
}

func TestLegacyLocationsOnlyWhenTheLayoutChanged(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("FYRWIRE_CONFIG", "")
	t.Setenv("FYRWIRE_STATE", "")
	restore := SetGOOSForTest("linux")
	if got := LegacyLocations(); len(got) != 0 {
		t.Errorf("linux layout is unchanged, nothing to migrate: %v", got)
	}
	restore()
	defer SetGOOSForTest("darwin")()
	if got := LegacyLocations(); len(got) == 0 {
		t.Error("a macOS user upgrading has files in the old place")
	}
}
