package platforms

import (
	"os/exec"
	"strings"

	"github.com/sixxhands/fyrwire/internal/node/model"
)

const windowsTaskName = "FyrWire"

type WindowsInstaller struct {
	Launcher     func(configPath string) []string
	BuildWarning func(exe string) string
}

func (installer WindowsInstaller) Install(configPath string, log func(string)) error {
	warnIfBuildDir(installer.Launcher, installer.BuildWarning, configPath, log)

	if _, err := exec.LookPath("schtasks"); err != nil {
		return model.Errorf("schtasks not found; cannot register a startup task")
	}

	command := SchtasksCommand(installer.Launcher(configPath))
	output, err := exec.Command(command[0], command[1:]...).CombinedOutput()
	if err != nil {
		return model.Errorf("schtasks failed: %s", strings.TrimSpace(string(output)))
	}

	log("registered scheduled task " + windowsTaskName + " (runs at logon)")
	return nil
}

func (WindowsInstaller) Uninstall(log func(string)) error {
	_ = exec.Command("schtasks", "/Delete", "/TN", windowsTaskName, "/F").Run()
	log("removed scheduled task " + windowsTaskName)
	return nil
}

func SchtasksCommand(parts []string) []string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.Contains(part, " ") {
			part = `"` + part + `"`
		}
		quoted = append(quoted, part)
	}

	return []string{
		"schtasks", "/Create", "/TN", windowsTaskName,
		"/SC", "ONLOGON", "/TR", strings.Join(quoted, " "), "/F",
	}
}
