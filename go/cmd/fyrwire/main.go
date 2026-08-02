package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sixxhands/fyrwire/internal/agent"
	"github.com/sixxhands/fyrwire/internal/banner"
	"github.com/sixxhands/fyrwire/internal/config"
	"github.com/sixxhands/fyrwire/internal/hostinfo"
	"github.com/sixxhands/fyrwire/internal/install"
	"github.com/sixxhands/fyrwire/internal/model"
	"github.com/sixxhands/fyrwire/internal/notify"
	"github.com/sixxhands/fyrwire/internal/paths"
	"github.com/sixxhands/fyrwire/internal/preset"
	"github.com/sixxhands/fyrwire/internal/provider"
	"github.com/sixxhands/fyrwire/internal/relay"
	"github.com/sixxhands/fyrwire/internal/setup"
	"github.com/sixxhands/fyrwire/internal/source"
)

const (
	cReset = "\x1b[0m"
	cDim   = "\x1b[38;2;120;130;140m"
	cOK    = "\x1b[38;2;110;220;150m"
	cBad   = "\x1b[38;2;240;110;110m"
	cWarn  = "\x1b[38;2;240;190;90m"
)

var useColour = os.Getenv("NO_COLOR") == "" && isTTY()

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func paint(c, s string) string {
	if !useColour {
		return s
	}
	return c + s + cReset
}

// Single error exit boundary. Everything below returns an error and lets main complain once.
func main() {
	cfgPath := flag.String("config", paths.ConfigFile(), "path to fyrwire.toml")
	statePath := flag.String("state", paths.StateFile(), "path to state.json")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "once":
		err = cmdOnce(*cfgPath, *statePath, false)
	case "run":
		err = cmdRun(*cfgPath, *statePath)
	case "doctor":
		err = cmdDoctor(*cfgPath, *statePath)
	case "status":
		err = cmdStatus(*statePath)
	case "presets":
		err = cmdPresets()
	case "sources":
		err = cmdSources()
	case "init":
		err = setup.Init(*cfgPath, len(args) > 1 && args[1] == "--force")
	case "add":
		err = setup.Add(*cfgPath)
	case "remove":
		if len(args) < 2 {
			err = model.Errorf("usage: fyrwire remove NAME [--keep-dns]")
		} else {
			err = setup.Remove(*cfgPath, args[1],
				len(args) > 2 && args[2] == "--keep-dns")
		}
	case "check":
		err = setup.Check(*cfgPath, 6*time.Second)
	case "setup":
		err = setup.Run(*cfgPath, len(args) > 1 && args[1] == "--force")
	case "install":
		err = install.Install(*cfgPath, logln)
	case "uninstall":
		err = install.Uninstall(logln)
	case "version":
		fmt.Println(banner.Render(useColour))
		return
	default:
		err = model.Errorf("unknown command %q — try `fyrwire -h`", args[0])
	}
	if err != nil {

		fmt.Fprintln(os.Stderr, paint(cBad, "fyrwire: ")+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(banner.Render(useColour))
	fmt.Println(`
  fyrwire [--config PATH] [--state PATH] COMMAND

    init        write a starter config
    setup       guided first-run setup — the only thing you need is a token
    add         add a service to an existing config
    remove      remove a service and its DNS record
    check       verify from OUTSIDE: resolve, then connect
    once        one reconcile cycle, then exit
    run         loop forever (this is what the service runs)
    doctor      check an existing setup, publish nothing
    status      what the agent last did
    presets     service presets, and what each client actually follows
    sources     available source and provider plug-ins
    install     run it in the background (systemd / launchd / Task Scheduler)
    uninstall   stop running it in the background
    version     print the wordmark`)
}

func logln(s string) { fmt.Println(s) }

func build(cfgPath, statePath string) (*agent.Agent, *config.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	src, err := source.Build(cfg.Source)
	if err != nil {
		return nil, nil, err
	}
	prov, err := provider.Build(cfg.Provider)
	if err != nil {
		return nil, nil, err
	}
	plan, err := cfg.Plan()
	if err != nil {
		return nil, nil, err
	}
	opt := agent.OptionsData{
		Relay: cfg.Node.Relay, RelayUDP: cfg.Node.RelayUDP,
		BindHost: cfg.Node.BindHost, TargetHost: cfg.Node.TargetHost,
		Log: logln, UDP: udpOptions(cfg.Node.UDP),
		Notifier: notifierFrom(cfg), NodeName: cfg.Node.AName,
	}
	return agent.New(src, prov, plan, statePath, opt), cfg, nil
}

func notifierFrom(cfg *config.Config) *notify.Notifier {
	if cfg.Alerts.Webhook == "" && cfg.Alerts.Command == "" {
		return nil
	}
	return &notify.Notifier{Webhook: cfg.Alerts.Webhook,
		Command: cfg.Alerts.Command, AfterFailures: cfg.Alerts.AfterFailures,
		Log: logln}
}

func udpOptions(u config.UDP) relay.UdpOptionsData {
	o := relay.UdpOptionsData{DropZeroLength: true}
	if u.DropZeroLength != nil {
		o.DropZeroLength = *u.DropZeroLength
	}
	if u.MaxSessions != nil {
		o.MaxSessions, o.MaxSessionsSet = *u.MaxSessions, true
	}
	if u.MaxPerIP != nil {
		o.MaxPerIP, o.MaxPerIPSet = *u.MaxPerIP, true
	}
	if u.NewPerSec != nil {
		o.NewPerSec, o.NewPerSecSet = *u.NewPerSec, true
	}
	if u.IdleNew != nil {
		o.IdleNew, o.IdleNewSet = time.Duration(*u.IdleNew)*time.Second, true
	}
	if u.IdleEstablished != nil {
		o.IdleEstablished = time.Duration(*u.IdleEstablished) * time.Second
		o.IdleEstablishedSet = true
	}
	if u.ReapInterval != nil {
		o.ReapInterval = time.Duration(*u.ReapInterval * float64(time.Second))
	}
	return o
}

func cmdOnce(cfgPath, statePath string, quiet bool) error {
	a, _, err := build(cfgPath, statePath)
	if err != nil {
		return err
	}
	defer a.Shutdown()
	r := a.Tick(false)
	if r.Err != nil {
		return r.Err
	}
	for _, c := range r.Changes {
		fmt.Println("  " + paint(cOK, "✓ ") + c)
	}
	if r.Unchanged {
		fmt.Println("  " + paint(cDim, "nothing changed — "+r.Host))
	}
	for _, s := range r.Skipped {
		fmt.Println("  " + paint(cWarn, "! ") + s +
			paint(cDim, " had no mapping; its record is unchanged"))
	}
	return nil
}

func cmdRun(cfgPath, statePath string) error {
	a, cfg, err := build(cfgPath, statePath)
	if err != nil {
		return err
	}
	defer a.Shutdown()
	interval := cfg.Agent.Interval
	if interval <= 0 {
		interval = 45
	}
	fmt.Print(banner.Render(useColour))
	fmt.Printf("\n  %s every %ds\n\n",
		paint(cDim, "reconciling"), interval)
	a.Run(time.Duration(interval)*time.Second,
		time.Duration(cfg.Agent.Jitter)*time.Second, 0)
	return nil
}

func cmdDoctor(cfgPath, statePath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	plan, err := cfg.Plan()
	if err != nil {
		return err
	}

	fmt.Printf("  %-24s %s\n", "config", cfgPath)
	fmt.Printf("  %-24s %s\n", "state", statePath)

	b := hostinfo.Current()
	healthy, why := b.Health()
	label := b.Name()
	if !healthy {
		label += "  LIMITED"
	}
	fmt.Printf("  %-24s %s\n", "platform", label)
	if why != "" {
		fmt.Println("    " + paint(cDim, why))
	}

	if addr := hostinfo.EgressAddress(); addr != "" {
		fmt.Printf("  %-24s %s\n", "egress address", addr)
	}

	ports, _ := b.ListeningPorts()
	listening := map[int]bool{}
	for _, p := range ports {
		listening[p.Port] = true
	}
	for _, svc := range plan.Services {
		mark := paint(cOK, "OK")
		if !listening[svc.InternalPort] {
			mark = paint(cWarn, fmt.Sprintf("WARN :%d nothing listening",
				svc.InternalPort))
		}
		fmt.Printf("  service %-16s %s\n", svc.Key, mark)
	}
	return nil
}

func cmdStatus(statePath string) error {
	s := agent.LoadState(statePath)
	fmt.Printf("  %-20s %s\n", "last address", or(s.LastHost, "—"))
	keys := make([]string, 0, len(s.LastPorts))
	for k := range s.LastPorts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-20s %d\n", k, s.LastPorts[k])
	}
	fmt.Printf("  %-20s %d\n", "publishes", s.TotalPublishes)
	fmt.Printf("  %-20s %s\n", "last change", or(s.LastChangeAt, "—"))
	if s.ConsecutiveFailures > 0 {
		fmt.Printf("  %-20s %s\n", "failing",
			paint(cBad, fmt.Sprintf("%d cycles — %s",
				s.ConsecutiveFailures, s.LastError)))
	}
	return nil
}

func cmdPresets() error {
	for _, g := range preset.Groups {
		items := preset.ByGroup(g)
		if len(items) == 0 {
			continue
		}
		fmt.Println("\n  " + paint(cDim, strings.ToUpper(g)))
		for _, p := range items {
			mark := paint(cOK, "auto")
			switch p.Follows {
			case "srv-setup":
				mark = paint(cWarn, "once")
			case "manual":
				mark = paint(cDim, "manual")
			}
			fmt.Printf("    %-22s %-5s %-6s %s\n", p.Key,
				fmt.Sprintf("%d", p.Port), p.Protocol, mark)
		}
	}
	fmt.Println("\n  " + paint(cDim, "auto   — the client follows the SRV record"))
	fmt.Println("  " + paint(cDim, "once   — read only when the server is ADDED; a rotation breaks it"))
	fmt.Println("  " + paint(cDim, "manual — nothing reads the SRV; hand out host:port yourself"))
	return nil
}

func cmdSources() error {
	s := source.Available()
	p := provider.Available()
	sort.Strings(s)
	sort.Strings(p)
	fmt.Println("  sources:   " + strings.Join(s, ", "))
	fmt.Println("  providers: " + strings.Join(p, ", "))
	return nil
}

func or(a, b string) string {
	if a == "" {
		return b
	}
	return a
}
