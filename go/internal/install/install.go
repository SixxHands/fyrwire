package install

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sixxhands/fyrwire/internal/model"
	"github.com/sixxhands/fyrwire/internal/paths"
)

const (
	label = "com.fyrwire.agent"
	unit  = "fyrwire.service"
	task  = "FyrWire"
)

func launcher(configPath string) []string {
	exe, err := os.Executable()
	if err != nil {
		exe = "fyrwire"
	}
	return []string{exe, "--config", configPath, "run"}
}

// Warn if someone ran 'fyrwire install' straight out of dist/ or build/. The next rebuild will delete the service binary out from under it.
func buildDirWarning(exe string) string {
	for _, d := range []string{"/dist/", "/build/", "/target/", "/_output/"} {
		if strings.Contains(exe, d) {
			return "the binary being installed is at " + exe + ",\n" +
				"  which is a build output directory — a later build may delete\n" +
				"  it, and the service will then fail to start. Copy it somewhere\n" +
				"  stable first:\n" +
				"    install -Dm755 " + exe + " ~/.local/bin/fyrwire\n" +
				"  and run `fyrwire install` from there."
		}
	}
	return ""
}

func systemdArg(a string) string {
	a = strings.ReplaceAll(a, "%", "%%")
	if strings.ContainsAny(a, " \"\\\t") {
		a = strings.ReplaceAll(a, "\\", "\\\\")
		a = strings.ReplaceAll(a, "\"", "\\\"")
		return "\"" + a + "\""
	}
	return a
}

func SystemdUnit(configPath string) string {
	args := launcher(configPath)
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, systemdArg(a))
	}
	return `[Unit]
Description=FyrWire — keep names pointed at a moving endpoint
# Not After=network-online.target: the VPN comes up long after the network does,
# and a hard dependency would just delay the first attempt. The agent retries on
# its own interval, so starting early and failing once is cheaper than waiting
# for a target that may never fire.
After=network.target

[Service]
Type=simple
ExecStart=` + strings.Join(quoted, " ") + `
Restart=always
RestartSec=15
# The token is NOT written here. A unit file is world-readable, and this would
# then be the one place a secret leaks from. Put it in a mode-600 file instead:
#   printf 'FYRWIRE_CF_TOKEN=...\n' > ~/.config/fyrwire/env && chmod 600 "$_"
EnvironmentFile=-%h/.config/fyrwire/env

[Install]
WantedBy=default.target
`
}

type plistDict struct {
	XMLName xml.Name `xml:"dict"`
	Inner   []byte   `xml:",innerxml"`
}

func LaunchdPlist(configPath string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>` + esc(label) + `</string>
    <key>ProgramArguments</key>
    <array>
`)
	for _, a := range launcher(configPath) {
		b.WriteString("        <string>" + esc(a) + "</string>\n")
	}
	b.WriteString(`    </array>
    <key>RunAtLoad</key>
    <true/>
    <!-- KeepAlive restarts the agent if it exits for any reason, which is what
         Restart=always does on systemd. SuccessfulExit=false respects a clean
         exit 0 as deliberate rather than fighting it. -->
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>ThrottleInterval</key>
    <integer>15</integer>
    <key>StandardOutPath</key>
    <string>` + esc(filepath.Join(paths.StateDir(), "fyrwire.log")) + `</string>
    <key>StandardErrorPath</key>
    <string>` + esc(filepath.Join(paths.StateDir(), "fyrwire.log")) + `</string>
</dict>
</plist>
`)
	return b.String()
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func SchtasksCommand(configPath string) []string {
	parts := launcher(configPath)
	quoted := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.Contains(p, " ") {
			p = `"` + p + `"`
		}
		quoted = append(quoted, p)
	}
	return []string{"schtasks", "/Create", "/TN", task, "/SC", "ONLOGON",
		"/TR", strings.Join(quoted, " "), "/F"}
}

func Install(configPath string, log func(string)) error {
	if w := buildDirWarning(launcher(configPath)[0]); w != "" {
		log("! " + w)
		log("")
	}
	switch runtime.GOOS {
	case "linux":
		dir := paths.ServiceDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		p := filepath.Join(dir, unit)
		if err := os.WriteFile(p, []byte(SystemdUnit(configPath)), 0o644); err != nil {
			return err
		}
		log("wrote " + p)
		if _, err := exec.LookPath("systemctl"); err == nil {
			_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		}
		log("next:")
		log("  systemctl --user enable --now " + unit)
		log("")
		log("  and, if this machine is headless or you log out:")
		log("    sudo loginctl enable-linger " + os.Getenv("USER"))
		log("  Without lingering, a user service stops when your last session")
		log("  ends — so DNS would only update while you happen to be logged in.")
		return nil
	case "darwin":
		dir := paths.ServiceDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		_ = os.MkdirAll(paths.StateDir(), 0o755)
		p := filepath.Join(dir, label+".plist")
		if err := os.WriteFile(p, []byte(LaunchdPlist(configPath)), 0o644); err != nil {
			return err
		}
		log("wrote " + p)
		log("next:  launchctl bootstrap gui/$UID " + p)
		log("")
		log("UNVERIFIED: this installer has never been run on macOS.")
		return nil
	case "windows":
		if _, err := exec.LookPath("schtasks"); err != nil {
			return model.Errorf("schtasks not found; cannot register a startup task")
		}
		cmd := SchtasksCommand(configPath)
		out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
		if err != nil {
			return model.Errorf("schtasks failed: %s", strings.TrimSpace(string(out)))
		}
		log("registered scheduled task " + task + " (runs at logon, as you)")
		log("")
		log("UNVERIFIED: this installer has never been run on Windows.")
		return nil
	}
	return model.Errorf(
		"no service installer for %s. FyrWire itself still runs — start "+
			"`fyrwire run` however this system starts things.", runtime.GOOS)
}

func Uninstall(log func(string)) error {
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("systemctl"); err == nil {
			_ = exec.Command("systemctl", "--user", "disable", "--now", unit).Run()
		}
		p := filepath.Join(paths.ServiceDir(), unit)
		if err := os.Remove(p); err == nil {
			log("removed " + p)
		}
		return nil
	case "darwin":
		p := filepath.Join(paths.ServiceDir(), label+".plist")
		if err := os.Remove(p); err == nil {
			log("removed " + p)
		}
		return nil
	case "windows":
		_ = exec.Command("schtasks", "/Delete", "/TN", task, "/F").Run()
		log("removed scheduled task " + task)
		return nil
	}
	return model.Errorf("nothing to uninstall on %s", runtime.GOOS)
}

var _ = plistDict{}
