package install

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/sixxhands/fyrwire/internal/daemon/install/platforms"
)

func Test_systemd_unit_quotes_space_in_path(t *testing.T) {
	line := execStart(t, platforms.SystemdUnit(Launcher("/home/a b/cfg.toml")))
	if !strings.Contains(line, `"/home/a b/cfg.toml"`) {
		t.Errorf("ExecStart = %q", line)
	}
}

func Test_systemd_unit_escapes_percent(t *testing.T) {
	line := execStart(t, platforms.SystemdUnit(Launcher("/home/50%/x.toml")))
	if !strings.Contains(line, "50%%") {
		t.Errorf("ExecStart = %q", line)
	}
}

func Test_systemd_unit_uses_environment_file(t *testing.T) {
	unit := platforms.SystemdUnit(Launcher("/x/cfg.toml"))
	if !strings.Contains(unit, "EnvironmentFile") {
		t.Error("expected EnvironmentFile in unit")
	}
}

func Test_launchd_plist_escapes_xml_chars(t *testing.T) {
	for _, path := range []string{"/x/cfg.toml", "/home/a&b/<c>.toml"} {
		var parsed any
		if err := xml.Unmarshal([]byte(platforms.LaunchdPlist(Launcher(path))), &parsed); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func Test_launchd_plist_contains_config_path_and_keep_alive(t *testing.T) {
	plist := platforms.LaunchdPlist(Launcher("/home/u/cfg.toml"))
	if !strings.Contains(plist, "/home/u/cfg.toml") {
		t.Error("config path missing")
	}
	if !strings.Contains(plist, "KeepAlive") {
		t.Error("KeepAlive missing")
	}
}

func Test_schtasks_registers_logon_task(t *testing.T) {
	command := platforms.SchtasksCommand(Launcher("/x/cfg.toml"))
	joined := strings.Join(command, " ")
	if !strings.Contains(joined, "ONLOGON") {
		t.Errorf("cmd = %v", command)
	}
	if strings.Contains(joined, "/RU") {
		t.Error("must not set /RU")
	}
}

func Test_schtasks_quotes_spaces_in_path(t *testing.T) {
	command := platforms.SchtasksCommand(Launcher(`C:\Users\a b\cfg.toml`))
	var tr string
	for i, arg := range command {
		if arg == "/TR" && i+1 < len(command) {
			tr = command[i+1]
		}
	}
	if tr == "" {
		t.Fatalf("no /TR in %v", command)
	}
	if !strings.Contains(tr, `"C:\Users\a b\cfg.toml"`) {
		t.Errorf("/TR = %q, path with space must be quoted", tr)
	}
}

func execStart(t *testing.T, unit string) string {
	t.Helper()
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			return line
		}
	}
	t.Fatal("no ExecStart in unit")
	return ""
}

func Test_build_directory_install_warns(t *testing.T) {
	for _, path := range []string{
		"/home/u/proj/dist/fyrwire",
		"/src/build/fyrwire",
		"/x/target/release/fyrwire",
		`C:\src\dist\fyrwire.exe`,
	} {
		if BuildDirWarning(path) == "" {
			t.Errorf("%s should be flagged", path)
		}
	}
	for _, path := range []string{"/usr/local/bin/fyrwire", "/home/u/.local/bin/fyrwire"} {
		if warning := BuildDirWarning(path); warning != "" {
			t.Errorf("%s should not warn: %s", path, warning)
		}
	}
}

func Test_factory_selects_installer_by_platform(t *testing.T) {
	factory := ServiceInstallerFactory{}
	if _, ok := factory.CreateForPlatform("linux").(platforms.SystemdInstaller); !ok {
		t.Error("linux should produce SystemdInstaller")
	}
	if _, ok := factory.CreateForPlatform("darwin").(platforms.LaunchdInstaller); !ok {
		t.Error("darwin should produce LaunchdInstaller")
	}
	if _, ok := factory.CreateForPlatform("windows").(platforms.WindowsInstaller); !ok {
		t.Error("windows should produce WindowsInstaller")
	}
	if _, ok := factory.CreateForPlatform("plan9").(platforms.UnsupportedInstaller); !ok {
		t.Error("unknown OS should produce UnsupportedInstaller")
	}
}
