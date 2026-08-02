package install

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestSystemdUnitSurvivesASpaceInThePath(t *testing.T) {
	unit := SystemdUnit("/home/a b/cfg.toml")
	line := execStart(t, unit)
	if !strings.Contains(line, `"/home/a b/cfg.toml"`) {
		t.Errorf("ExecStart = %q", line)
	}
}

func TestSystemdUnitEscapesPercent(t *testing.T) {
	line := execStart(t, SystemdUnit("/home/50%/x.toml"))
	if !strings.Contains(line, "50%%") {
		t.Errorf("ExecStart = %q", line)
	}
}

func TestSystemdUnitDoesNotEmbedTheToken(t *testing.T) {
	unit := SystemdUnit("/x/cfg.toml")
	if !strings.Contains(unit, "EnvironmentFile") {
		t.Error("the token must come from a mode-600 file, not the unit")
	}
}

func TestLaunchdPlistIsWellFormedEvenWithXmlMetacharacters(t *testing.T) {
	for _, path := range []string{"/x/cfg.toml", "/home/a&b/<c>.toml"} {
		var v any
		if err := xml.Unmarshal([]byte(LaunchdPlist(path)), &v); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestLaunchdPlistCarriesTheConfigPath(t *testing.T) {
	p := LaunchdPlist("/home/u/cfg.toml")
	if !strings.Contains(p, "/home/u/cfg.toml") {
		t.Error("the config path must reach the agent")
	}
	if !strings.Contains(p, "KeepAlive") {
		t.Error("the agent must be restarted if it exits")
	}
}

func TestSchtasksRegistersALogonTaskNotASystemService(t *testing.T) {
	cmd := SchtasksCommand("/x/cfg.toml")
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "ONLOGON") {
		t.Errorf("cmd = %v", cmd)
	}
	if strings.Contains(joined, "/RU") {
		t.Error("must not request a different run-as user")
	}
}

func TestSchtasksPassesTheCommandAsOneArgument(t *testing.T) {
	cmd := SchtasksCommand(`C:\Users\a b\cfg.toml`)
	var tr string
	for i, a := range cmd {
		if a == "/TR" && i+1 < len(cmd) {
			tr = cmd[i+1]
		}
	}
	if tr == "" {
		t.Fatalf("no /TR in %v", cmd)
	}
	if !strings.Contains(tr, `"C:\Users\a b\cfg.toml"`) {
		t.Errorf("/TR = %q — a path with a space must be quoted", tr)
	}
}

func execStart(t *testing.T, unit string) string {
	t.Helper()
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			return line
		}
	}
	t.Fatal("no ExecStart in the unit")
	return ""
}
