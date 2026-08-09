package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/sixxhands/fyrwire/internal/cli/banner"
	"github.com/sixxhands/fyrwire/internal/cli/setup"
	"github.com/sixxhands/fyrwire/internal/daemon/install"
	"github.com/sixxhands/fyrwire/internal/daemon/paths"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

var version = "dev"

type commandRunner func(args []string, configPath, statePath string) error

// Every subcommand is here so main() does not turn into a 200-line switch monster.
var commandTable = map[string]commandRunner{
	"once": func(args []string, configPath, statePath string) error {
		return runOnceCommand(configPath, statePath)
	},
	"run": func(args []string, configPath, statePath string) error {
		return runDaemonCommand(configPath, statePath)
	},
	"doctor": func(args []string, configPath, statePath string) error {
		return runDoctorCommand(configPath, statePath)
	},
	"status": func(args []string, configPath, statePath string) error {
		return showStatusCommand(statePath)
	},
	"presets": func(args []string, configPath, statePath string) error {
		return listPresetsCommand()
	},
	"sources": func(args []string, configPath, statePath string) error {
		return listSourcesCommand()
	},
	"init": func(args []string, configPath, statePath string) error {
		return setup.Init(configPath, hasForceFlag(args))
	},
	"add": func(args []string, configPath, statePath string) error {
		return setup.Add(configPath)
	},
	"remove": func(args []string, configPath, statePath string) error {
		return removeServiceCommand(configPath, args[1:])
	},
	"check": func(args []string, configPath, statePath string) error {
		return setup.Check(configPath, 6*time.Second)
	},
	"setup": func(args []string, configPath, statePath string) error {
		return setup.Run(configPath, hasForceFlag(args))
	},
	"install": func(args []string, configPath, statePath string) error {
		return install.ServiceInstallerFactory{}.Create().Install(configPath, logLine)
	},
	"uninstall": func(args []string, configPath, statePath string) error {
		return install.ServiceInstallerFactory{}.Create().Uninstall(logLine)
	},
	"version": func(args []string, configPath, statePath string) error {
		fmt.Print(banner.Render(colorEnabled()))
		fmt.Printf("\n  %s\n", paintText(colorDim, version))
		return nil
	},
}

// Single error exit, everything below returns s single error and lets main make a complaint once.
func main() {
	configPath := flag.String("config", paths.ConfigFile(), "path to fyrwire.toml")
	statePath := flag.String("state", paths.StateFile(), "path to state.json")
	flag.Usage = printUsage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		printUsage()
		os.Exit(2)
	}

	if err := runCommand(args, *configPath, *statePath); err != nil {
		fmt.Fprintln(os.Stderr, paintText(colorBad, "fyrwire: ")+err.Error())
		os.Exit(1)
	}
}

func runCommand(args []string, configPath, statePath string) error {
	runner, ok := commandTable[args[0]]
	if !ok {
		return model.Errorf("unknown command %q (run `fyrwire -h` for help)", args[0])
	}

	return runner(args, configPath, statePath)
}

func hasForceFlag(args []string) bool {
	return len(args) > 1 && args[1] == "--force"
}

func printUsage() {
	fmt.Print(banner.Render(colorEnabled()))
	fmt.Println(`
  fyrwire [--config PATH] [--state PATH] COMMAND

    init        write a starter config
    setup       guided first-run setup
    add         add a service to an existing config
    remove      remove a service and its DNS record (--keep-dns, --yes)
    check       check published records and test TCP connections
    once        run one update cycle and exit
    run         loop forever (used by background service)
    doctor      check local config, platform, and listening ports
    status      show last published host, ports, and error state
    presets     list service presets and client SRV support
    sources     list available sources and DNS providers
    install     install as a user service (systemd / launchd / Task Scheduler)
    uninstall   remove the user service
    version     print version`)
}
