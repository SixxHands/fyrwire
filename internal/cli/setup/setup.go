package setup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sixxhands/fyrwire/internal/cli/banner"
	"github.com/sixxhands/fyrwire/internal/cli/detect"
	"github.com/sixxhands/fyrwire/internal/node/model"
	"github.com/sixxhands/fyrwire/internal/node/preset"
)

const (
	reset = "\x1b[0m"
	dim   = "\x1b[38;2;120;130;140m"
	ok    = "\x1b[38;2;110;220;150m"
	bad   = "\x1b[38;2;240;110;110m"
	warn  = "\x1b[38;2;240;190;90m"
	text  = "\x1b[38;2;235;235;235m"
)

var inputReader = bufio.NewReader(os.Stdin)

func say(format string, args ...any)  { fmt.Printf(format+"\n", args...) }
func good(format string, args ...any) { say("  "+ok+"✓"+reset+" "+format, args...) }
func fail(format string, args ...any) { say("  "+bad+"✗"+reset+" "+format, args...) }
func note(format string, args ...any) { say("  "+warn+"!"+reset+" "+format, args...) }

func useBannerColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}

	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func ask(prompt, fallback string) string {
	if fallback != "" {
		fmt.Printf("  %s [%s%s%s]: ", prompt, dim, fallback, reset)
	} else {
		fmt.Printf("  %s: ", prompt)
	}

	line, _ := inputReader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback
	}

	return line
}

func askYesNo(prompt string, fallback bool) bool {
	hint := "y/N"
	if fallback {
		hint = "Y/n"
	}

	fmt.Printf("  %s [%s%s%s]: ", prompt, dim, hint, reset)
	line, _ := inputReader.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return fallback
	}

	return strings.HasPrefix(line, "y")
}

type serviceChoice struct {
	name         string
	presetKey    string
	srvName      string
	protocol     string
	record       string
	internalPort int
}

func Run(configPath string, force bool) error {
	if _, err := os.Stat(configPath); err == nil && !force {
		return model.Errorf("%s already exists; pass --force to overwrite or use `fyrwire add`", configPath)
	}

	fmt.Print(banner.Render(useBannerColor()))
	say("")

	sourceType, gateway := detectSource()
	providerBlock, zoneName, tokenEnv, ttl, err := chooseProvider()
	if err != nil {
		return err
	}

	services := askServices(zoneName)
	aName := ask("Name for this machine's A record", "node."+zoneName)

	content := renderConfig(tokenEnv, sourceType, gateway, providerBlock, aName, ttl, services)
	if err := writeConfigFile(configPath, content); err != nil {
		return err
	}

	printNextSteps(configPath, tokenEnv)
	return nil
}

func detectSource() (string, string) {
	say("%sFinding your gateway%s", text, reset)
	gateway, public, reason := detect.FindGateway()
	if gateway != "" {
		good("%s speaks NAT-PMP (%s)", gateway, reason)
		good("public address is %s", public)
		return "natpmp", gateway
	}

	fail("no gateway answered NAT-PMP")
	say("    %sCheck that VPN port forwarding is enabled, or use the command source.%s", dim, reset)
	if askYesNo("Read the port from a command instead?", true) {
		return "command", ""
	}

	return "natpmp", ask("Gateway address to use", "10.2.0.1")
}

func askServices(zoneName string) []serviceChoice {
	say("")
	say("%sWhat are you exposing?%s", text, reset)

	listening := map[int]bool{}
	for _, portItem := range detect.ListeningPorts() {
		listening[portItem.Port] = true
	}

	var services []serviceChoice
	for {
		choice, valid := promptOneService(zoneName, listening)
		if !valid {
			continue
		}

		services = append(services, choice)
		if !askYesNo("Add another?", false) {
			return services
		}
	}
}

func promptOneService(zoneName string, listening map[int]bool) (serviceChoice, bool) {
	key := ask("Preset (or `list`, or `custom`)", "minecraft")
	if key == "list" {
		printPresets()
		return serviceChoice{}, false
	}

	if key == "custom" {
		choice := askCustom(zoneName)
		return choice, choice.name != ""
	}

	item, found := preset.Get(key)
	if !found {
		fail("no preset called %q (type `list` to see them)", key)
		return serviceChoice{}, false
	}

	choice := serviceChoice{
		name:         item.Key,
		presetKey:    item.Key,
		protocol:     item.Protocol,
		record:       item.Record,
		internalPort: askPort(item, listening),
	}

	label := ask("Subdomain players will use", strings.Split(zoneName, ".")[0])
	choice.srvName = fmt.Sprintf("_%s._%s.%s.%s", item.Service, item.Protocol, label, zoneName)
	printPresetCaveat(item)
	return choice, true
}

func printPresetCaveat(item preset.PresetData) {
	switch item.Follows {
	case "srv-setup":
		note("%s reads the SRV record once on setup; a port rotation breaks the saved entry.", item.Label)
	case "manual":
		note("%s does not read SRV records (A record still tracks your IP).", item.Label)
	}

	if item.Note != "" {
		say("    %s%s%s", dim, item.Note, reset)
	}
}

func askPort(item preset.PresetData, listening map[int]bool) int {
	fallback := strconv.Itoa(item.Port)
	if len(listening) > 0 && !listening[item.Port] {
		var openPorts []int
		for port := range listening {
			openPorts = append(openPorts, port)
		}
		sort.Ints(openPorts)

		if len(openPorts) > 0 {
			say("    %snothing on :%d. Listening here: %v%s", dim, item.Port, openPorts, reset)
		}
	}

	for {
		value := ask("Local port it listens on", fallback)
		port, err := strconv.Atoi(value)
		if err == nil && port > 0 && port < 65536 {
			return port
		}
		fail("%q is not a valid port (1..65535)", value)
	}
}

func askCustom(zone string) serviceChoice {
	name := ask("Service name", "")
	if name == "" {
		return serviceChoice{}
	}

	label := ask("SRV service label (e.g. minecraft, http)", name)
	if err := preset.CheckServiceLabel(label); err != nil {
		fail("%v", err)
		return serviceChoice{}
	}

	protocol := ask("Protocol (tcp/udp)", "tcp")
	if protocol != "tcp" && protocol != "udp" {
		fail("protocol must be tcp or udp")
		return serviceChoice{}
	}

	port, err := strconv.Atoi(ask("Local port", "0"))
	if err != nil || port < 1 || port > 65535 {
		fail("port must be 1..65535")
		return serviceChoice{}
	}

	subdomain := ask("Subdomain", strings.Split(zone, ".")[0])
	return serviceChoice{
		name:         name,
		protocol:     protocol,
		record:       "srv",
		internalPort: port,
		srvName:      fmt.Sprintf("_%s._%s.%s.%s", label, protocol, subdomain, zone),
	}
}

func printPresets() {
	for _, groupName := range preset.Groups {
		items := preset.ByGroup(groupName)
		if len(items) == 0 {
			continue
		}

		say("    %s%s%s", dim, strings.ToUpper(groupName), reset)
		for _, item := range items {
			mark := ok + "auto" + reset
			switch item.Follows {
			case "srv-setup":
				mark = warn + "once" + reset
			case "manual":
				mark = dim + "manual" + reset
			}
			say("      %-20s %-6d %-4s %s", item.Key, item.Port, item.Protocol, mark)
		}
	}
}

func writeConfigFile(configPath, content string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}

	return os.WriteFile(configPath, []byte(content), 0o644)
}

func printNextSteps(configPath, tokenEnv string) {
	say("")
	good("wrote %s", configPath)

	if os.Getenv(tokenEnv) == "" {
		note("export %s before running fyrwire:", tokenEnv)
		say("    %sexport %s=\"...\"%s", dim, tokenEnv, reset)
	}

	say("")
	say("  %sNext:%s", text, reset)
	say("    fyrwire once       %s(publish once and print changes)%s", dim, reset)
	say("    fyrwire install    %s(run in the background)%s", dim, reset)
}
