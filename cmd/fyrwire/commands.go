package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sixxhands/fyrwire/internal/cli/banner"
	"github.com/sixxhands/fyrwire/internal/cli/setup"
	"github.com/sixxhands/fyrwire/internal/daemon/agent"
	"github.com/sixxhands/fyrwire/internal/network/hostinfo"
	"github.com/sixxhands/fyrwire/internal/network/provider"
	"github.com/sixxhands/fyrwire/internal/network/source"
	"github.com/sixxhands/fyrwire/internal/node/config"
	"github.com/sixxhands/fyrwire/internal/node/model"
	"github.com/sixxhands/fyrwire/internal/node/preset"
)

func runOnceCommand(configPath, statePath string) error {
	runner, _, err := buildAgent(configPath, statePath)
	if err != nil {
		return err
	}
	defer runner.Stop()

	tickResult := runner.Tick(false)
	if tickResult.Err != nil {
		return tickResult.Err
	}

	printTickResult(tickResult)
	return nil
}

func printTickResult(tickResult agent.TickResult) {
	for _, change := range tickResult.Changes {
		fmt.Println("  " + paintText(colorOk, "✓ ") + change)
	}

	if tickResult.Unchanged {
		fmt.Println("  " + paintText(colorDim, "nothing changed ("+tickResult.Host+")"))
	}

	for _, serviceKey := range tickResult.Skipped {
		reason := fallbackText(tickResult.Reasons[serviceKey], "no mapping")
		fmt.Println("  " + paintText(colorWarn, "! ") + serviceKey +
			paintText(colorDim, " skipped ("+reason+"); record unchanged"))
	}
}

func runDaemonCommand(configPath, statePath string) error {
	runner, loaded, err := buildAgent(configPath, statePath)
	if err != nil {
		return err
	}
	defer runner.Stop()

	intervalSeconds := loaded.Agent.Interval
	if intervalSeconds <= 0 {
		intervalSeconds = 45
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	fmt.Print(banner.Render(colorEnabled()))
	fmt.Printf("\n  %s every %ds\n\n", paintText(colorDim, "updating"), intervalSeconds)

	runner.RunContext(
		ctx,
		time.Duration(intervalSeconds)*time.Second,
		time.Duration(loaded.Agent.Jitter)*time.Second,
		0,
	)
	return nil
}

func runDoctorCommand(configPath, statePath string) error {
	loaded, err := config.Load(configPath)
	if err != nil {
		return err
	}

	nodePlan, err := loaded.Plan()
	if err != nil {
		return err
	}

	fmt.Printf("  %-24s %s\n", "config", configPath)
	fmt.Printf("  %-24s %s\n", "state", statePath)

	backend := hostinfo.HostBackendFactory{}.Create()
	printPlatformHealth(backend)

	if address := hostinfo.EgressAddress(); address != "" {
		fmt.Printf("  %-24s %s\n", "egress address", address)
	}

	printDoctorServices(backend, nodePlan.Services)
	return nil
}

func printPlatformHealth(backend hostinfo.IHostBackend) {
	healthy, reason := backend.Health()
	label := backend.Name()
	if !healthy {
		label += "  LIMITED"
	}

	fmt.Printf("  %-24s %s\n", "platform", label)
	if reason != "" {
		fmt.Println("    " + paintText(colorDim, reason))
	}
}

func printDoctorServices(backend hostinfo.IHostBackend, services []model.ServiceData) {
	ports, _ := backend.ListeningPorts()
	listening := map[int]bool{}
	for _, portItem := range ports {
		listening[portItem.Port] = true
	}

	for _, service := range services {
		mark := paintText(colorOk, "OK")
		if !listening[service.InternalPort] {
			mark = paintText(colorWarn, fmt.Sprintf("WARN :%d nothing listening", service.InternalPort))
		}
		fmt.Printf("  service %-16s %s\n", service.Key, mark)
	}
}

func showStatusCommand(statePath string) error {
	state := agent.LoadState(statePath)
	fmt.Printf("  %-20s %s\n", "last address", fallbackText(state.LastHost, "-"))

	keys := make([]string, 0, len(state.LastPorts))
	for serviceKey := range state.LastPorts {
		keys = append(keys, serviceKey)
	}
	sort.Strings(keys)

	for _, serviceKey := range keys {
		fmt.Printf("  %-20s %d\n", serviceKey, state.LastPorts[serviceKey])
	}

	fmt.Printf("  %-20s %d\n", "publishes", state.TotalPublishes)
	fmt.Printf("  %-20s %s\n", "last change", fallbackText(state.LastChangeAt, "-"))

	if state.ConsecutiveFailures > 0 {
		message := fmt.Sprintf("%d cycles: %s", state.ConsecutiveFailures, state.LastError)
		fmt.Printf("  %-20s %s\n", "failing", paintText(colorBad, message))
	}

	return nil
}

func listPresetsCommand() error {
	for _, groupName := range preset.Groups {
		items := preset.ByGroup(groupName)
		if len(items) == 0 {
			continue
		}

		fmt.Println("\n  " + paintText(colorDim, strings.ToUpper(groupName)))
		for _, item := range items {
			mark := formatPresetFollows(item.Follows)
			fmt.Printf("    %-22s %-5d %-6s %s\n", item.Key, item.Port, item.Protocol, mark)
		}
	}

	fmt.Println("\n  " + paintText(colorDim, "auto   : client follows the SRV record"))
	fmt.Println("  " + paintText(colorDim, "once   : read only when added; a rotation breaks it"))
	fmt.Println("  " + paintText(colorDim, "manual : nothing reads SRV; hand out host:port yourself"))
	return nil
}

func formatPresetFollows(follows string) string {
	switch follows {
	case "srv-setup":
		return paintText(colorWarn, "once")
	case "manual":
		return paintText(colorDim, "manual")
	default:
		return paintText(colorOk, "auto")
	}
}

func listSourcesCommand() error {
	sources := source.Available()
	providers := provider.Available()
	sort.Strings(sources)
	sort.Strings(providers)

	fmt.Println("  sources:   " + strings.Join(sources, ", "))
	fmt.Println("  providers: " + strings.Join(providers, ", "))
	return nil
}

func removeServiceCommand(configPath string, args []string) error {
	if len(args) < 1 {
		return model.Errorf("usage: fyrwire remove NAME [--keep-dns] [--yes]")
	}

	keepDns := false
	assumeYes := false
	for _, arg := range args[1:] {
		switch arg {
		case "--keep-dns":
			keepDns = true
		case "--yes", "-y":
			assumeYes = true
		}
	}

	return setup.Remove(configPath, args[0], keepDns, assumeYes)
}
