package install

import (
	"os"
	"strings"
)

type IServiceInstaller interface {
	Install(configPath string, log func(string)) error
	Uninstall(log func(string)) error
}

func Launcher(configPath string) []string {
	exe, err := os.Executable()
	if err != nil {
		exe = "fyrwire"
	}

	return []string{exe, "--config", configPath, "run"}
}

// Warn if someone ran 'fyrwire install' straight out of dist/ or build/. The next rebuild will delete the service binary
func BuildDirWarning(exe string) string {
	slashed := strings.ReplaceAll(exe, "\\", "/")
	for _, dir := range []string{"/dist/", "/build/", "/target/", "/_output/"} {
		if strings.Contains(slashed, dir) {
			return "the binary being installed is at " + exe + ",\n" +
				"  which is a build output directory. Copy it somewhere stable first:\n" +
				"    install -Dm755 " + exe + " ~/.local/bin/fyrwire\n" +
				"  and run `fyrwire install` from there."
		}
	}

	return ""
}
