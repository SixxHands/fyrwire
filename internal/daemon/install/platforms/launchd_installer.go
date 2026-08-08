package platforms

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"

	"github.com/sixxhands/fyrwire/internal/daemon/paths"
)

const launchdLabel = "com.fyrwire.agent"

type LaunchdInstaller struct {
	Launcher     func(configPath string) []string
	BuildWarning func(exe string) string
}

func (installer LaunchdInstaller) Install(configPath string, log func(string)) error {
	warnIfBuildDir(installer.Launcher, installer.BuildWarning, configPath, log)

	dir := paths.ServiceDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	_ = os.MkdirAll(paths.StateDir(), 0o755)
	plistPath := filepath.Join(dir, launchdLabel+".plist")
	content := LaunchdPlist(installer.Launcher(configPath))
	if err := os.WriteFile(plistPath, []byte(content), 0o644); err != nil {
		return err
	}

	log("wrote " + plistPath)
	log("next: launchctl bootstrap gui/$UID " + plistPath)
	return nil
}

func (LaunchdInstaller) Uninstall(log func(string)) error {
	plistPath := filepath.Join(paths.ServiceDir(), launchdLabel+".plist")
	if err := os.Remove(plistPath); err == nil {
		log("removed " + plistPath)
	}

	return nil
}

func LaunchdPlist(args []string) string {
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>` + escapeXml(launchdLabel) + `</string>
    <key>ProgramArguments</key>
    <array>
`)

	for _, arg := range args {
		builder.WriteString("        <string>" + escapeXml(arg) + "</string>\n")
	}

	logPath := escapeXml(filepath.Join(paths.StateDir(), "fyrwire.log"))
	builder.WriteString(`    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>ThrottleInterval</key>
    <integer>15</integer>
    <key>StandardOutPath</key>
    <string>` + logPath + `</string>
    <key>StandardErrorPath</key>
    <string>` + logPath + `</string>
</dict>
</plist>
`)
	return builder.String()
}

func escapeXml(text string) string {
	var builder strings.Builder
	_ = xml.EscapeText(&builder, []byte(text))
	return builder.String()
}
