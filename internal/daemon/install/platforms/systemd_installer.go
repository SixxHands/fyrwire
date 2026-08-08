package platforms

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sixxhands/fyrwire/internal/daemon/paths"
)

const systemdUnitName = "fyrwire.service"

type SystemdInstaller struct {
	Launcher     func(configPath string) []string
	BuildWarning func(exe string) string
}

func (installer SystemdInstaller) Install(configPath string, log func(string)) error {
	warnIfBuildDir(installer.Launcher, installer.BuildWarning, configPath, log)

	dir := paths.ServiceDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	unitPath := filepath.Join(dir, systemdUnitName)
	content := SystemdUnit(installer.Launcher(configPath))
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		return err
	}

	log("wrote " + unitPath)
	if _, err := exec.LookPath("systemctl"); err == nil {
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	}

	log("next:")
	log("  systemctl --user enable --now " + systemdUnitName)
	log("  sudo loginctl enable-linger " + os.Getenv("USER") + "  (for headless hosts)")
	return nil
}

func (SystemdInstaller) Uninstall(log func(string)) error {
	if _, err := exec.LookPath("systemctl"); err == nil {
		_ = exec.Command("systemctl", "--user", "disable", "--now", systemdUnitName).Run()
	}

	unitPath := filepath.Join(paths.ServiceDir(), systemdUnitName)
	if err := os.Remove(unitPath); err == nil {
		log("removed " + unitPath)
	}

	return nil
}

func SystemdUnit(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, escapeSystemdArg(arg))
	}

	return `[Unit]
Description=FyrWire dynamic port and DNS updater
After=network.target

[Service]
Type=simple
ExecStart=` + strings.Join(quoted, " ") + `
Restart=always
RestartSec=15
EnvironmentFile=-%h/.config/fyrwire/env

[Install]
WantedBy=default.target
`
}

func escapeSystemdArg(arg string) string {
	arg = strings.ReplaceAll(arg, "%", "%%")
	if strings.ContainsAny(arg, " \"\\\t") {
		arg = strings.ReplaceAll(arg, "\\", "\\\\")
		arg = strings.ReplaceAll(arg, "\"", "\\\"")
		return "\"" + arg + "\""
	}

	return arg
}

func warnIfBuildDir(
	launcher func(string) []string,
	buildWarning func(string) string,
	configPath string,
	log func(string),
) {
	if launcher == nil || buildWarning == nil || log == nil {
		return
	}

	args := launcher(configPath)
	if len(args) == 0 {
		return
	}

	if warning := buildWarning(args[0]); warning != "" {
		log("! " + warning)
		log("")
	}
}
