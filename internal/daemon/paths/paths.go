package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

const (
	app        = "fyrwire"
	appDisplay = "FyrWire"
)

var goosOverride string

func goos() string {
	if goosOverride != "" {
		return goosOverride
	}
	return runtime.GOOS
}

func SetGoosForTest(v string) func() {
	prev := goosOverride
	goosOverride = v
	return func() { goosOverride = prev }
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func ConfigDir() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, app)
	}
	switch goos() {
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(home(), "AppData", "Roaming")
		}
		return filepath.Join(base, appDisplay)
	case "darwin":
		return filepath.Join(home(), "Library", "Application Support", appDisplay)
	}
	return filepath.Join(home(), ".config", app)
}

func StateDir() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, app)
	}
	switch goos() {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(home(), "AppData", "Local")
		}
		return filepath.Join(base, appDisplay)
	case "darwin":
		return filepath.Join(home(), "Library", "Application Support", appDisplay)
	}
	return filepath.Join(home(), ".local", "state", app)
}

func ConfigFile() string {
	if v := os.Getenv("FYRWIRE_CONFIG"); v != "" {
		return v
	}
	return filepath.Join(ConfigDir(), "fyrwire.toml")
}

func StateFile() string {
	if v := os.Getenv("FYRWIRE_STATE"); v != "" {
		return v
	}
	return filepath.Join(StateDir(), "state.json")
}

func ServiceDir() string {
	switch goos() {
	case "darwin":
		return filepath.Join(home(), "Library", "LaunchAgents")
	case "windows":
		return ""
	}
	return filepath.Join(home(), ".config", "systemd", "user")
}

func LegacyLocations() [][2]string {
	var out [][2]string
	oldConfig := filepath.Join(home(), ".config", app, "fyrwire.toml")
	oldState := filepath.Join(home(), ".local", "state", app, "state.json")
	if oldConfig != ConfigFile() {
		out = append(out, [2]string{oldConfig, ConfigFile()})
	}
	if oldState != StateFile() {
		out = append(out, [2]string{oldState, StateFile()})
	}
	return out
}
