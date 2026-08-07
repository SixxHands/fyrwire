package manual

import (
	"context"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sixxhands/fyrwire/internal/network/source"
	"github.com/sixxhands/fyrwire/internal/node/config"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type CommandSource struct {
	Command     string
	Host        string
	HostCommand string
	Timeout     int
}

func (item *CommandSource) Name() string {
	return "command"
}

func (item *CommandSource) FindEndpoints(requests []model.PortRequestData) (model.EndpointData, error) {
	output, err := runShellCommand(item.Command, item.Timeout)
	if err != nil {
		return model.EndpointData{}, model.Errorf("source command failed: %v", err)
	}

	host, port, err := readEndpointText(strings.TrimSpace(output))
	if err != nil {
		return model.EndpointData{}, err
	}

	host, err = item.findHost(host)
	if err != nil {
		return model.EndpointData{}, err
	}

	endpoints := model.EndpointData{
		Host:    host,
		Ports:   map[string]int{},
		Partial: map[string]bool{},
	}
	for _, request := range requests {
		endpoints.Ports[request.Key] = port
	}

	return endpoints, nil
}

func (item *CommandSource) findHost(fallbackHost string) (string, error) {
	host := fallbackHost
	if item.Host != "" {
		host = item.Host
	}

	if item.HostCommand != "" {
		output, err := runShellCommand(item.HostCommand, item.Timeout)
		if err != nil {
			return "", model.Errorf("host_command failed: %v", err)
		}
		host = strings.TrimSpace(output)
	}

	if host == "" {
		return "", model.Errorf(
			"source command printed a port with no address, and neither host nor host_command is set")
	}

	return host, nil
}

func readEndpointText(text string) (string, int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", 0, model.Errorf("source command printed nothing")
	}

	if strings.ContainsAny(text, " \t") {
		return "", 0, model.Errorf(
			"source command printed %q; expected only a port or host:port", truncateLine(text))
	}

	host := ""
	portText := text
	if splitIndex := strings.LastIndex(text, ":"); splitIndex >= 0 {
		host = strings.TrimSpace(text[:splitIndex])
		portText = strings.TrimSpace(text[splitIndex+1:])
	}

	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, model.Errorf("cannot read port from %q (must be 1..65535)", truncateLine(text))
	}

	if host != "" && !isValidHost(host) {
		return "", 0, model.Errorf("%q is not a valid address or hostname", host)
	}

	return host, port, nil
}

func truncateLine(text string) string {
	if newline := strings.IndexByte(text, '\n'); newline >= 0 {
		text = text[:newline]
	}

	if len(text) > 80 {
		text = text[:80] + "..."
	}

	return text
}

func isValidHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}

	if len(host) > 253 {
		return false
	}

	for _, char := range host {
		validChar := char == '.' || char == '-' || char == '_' ||
			(char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9')
		if !validChar {
			return false
		}
	}

	return strings.Contains(host, ".")
}

func runShellCommand(commandText string, timeoutSeconds int) (string, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 15
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	var process *exec.Cmd
	if runtime.GOOS == "windows" {
		process = exec.CommandContext(ctx, "cmd", "/C", commandText)
	} else {
		process = exec.CommandContext(ctx, "sh", "-c", commandText)
	}

	output, err := process.Output()
	if ctx.Err() != nil {
		return string(output), model.Errorf("command timed out after %ds", timeoutSeconds)
	}

	return string(output), err
}

func init() {
	source.Register("command", []config.OptionRule{
		{Name: "command", Required: true, Kind: "string"},
		{Name: "host", Kind: "string"},
		{Name: "host_command", Kind: "string"},
		{Name: "timeout", Kind: "int"},
	}, buildCommandSource)
}

func buildCommandSource(section config.Section, _ func(string)) (source.IPortSource, error) {
	commandText, err := section.String("command")
	if err != nil {
		return nil, err
	}

	host, _ := section.String("host")
	hostCommand, _ := section.String("host_command")
	return &CommandSource{
		Command:     commandText,
		Host:        host,
		HostCommand: hostCommand,
		Timeout:     section.Int("timeout", 15),
	}, nil
}
