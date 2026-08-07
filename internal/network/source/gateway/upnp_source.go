package gateway

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/sixxhands/fyrwire/internal/network/source"
	"github.com/sixxhands/fyrwire/internal/node/config"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type UpnpSource struct {
	Binary  string
	Timeout int
}

func (source *UpnpSource) Name() string {
	return "upnp"
}

var (
	upnpExternalIp = regexp.MustCompile(`ExternalIPAddress\s*=\s*([0-9.]+)`)
	upnpMapped     = regexp.MustCompile(`(?i)external\s+[0-9.]+:(\d+)`)
)

func (source *UpnpSource) runUpnpc(args ...string) (string, error) {
	binary := source.Binary
	if binary == "" {
		binary = "upnpc"
	}

	if _, err := exec.LookPath(binary); err != nil {
		return "", model.Errorf("%s is not installed (upnp source requires miniupnpc)", binary)
	}

	timeoutSeconds := source.Timeout
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if ctx.Err() != nil {
		return string(output), model.Errorf("%s timed out after %ds", binary, timeoutSeconds)
	}

	return string(output), err
}

func (source *UpnpSource) FindEndpoints(requests []model.PortRequestData) (model.EndpointData, error) {
	host, err := source.readExternalAddress()
	if err != nil {
		return model.EndpointData{}, err
	}

	endpoints := model.EndpointData{
		Host:    host,
		Ports:   map[string]int{},
		Partial: map[string]bool{},
	}

	for _, request := range requests {
		port, ok := source.mapPort(request)
		if !ok {
			endpoints.Partial[request.Key] = true
			continue
		}
		endpoints.Ports[request.Key] = port
	}

	if len(endpoints.Ports) == 0 && len(requests) > 0 {
		return model.EndpointData{}, model.Errorf(
			"router granted no ports; check that UPnP is enabled")
	}

	return endpoints, nil
}

func (source *UpnpSource) readExternalAddress() (string, error) {
	output, err := source.runUpnpc("-s")
	if err != nil && output == "" {
		return "", err
	}

	match := upnpExternalIp.FindStringSubmatch(output)
	if match == nil {
		return "", model.Errorf(
			"could not read external address from upnpc; check that UPnP IGD is enabled")
	}

	return match[1], nil
}

func (source *UpnpSource) mapPort(request model.PortRequestData) (int, bool) {
	protocol := "TCP"
	if request.Protocol == "udp" {
		protocol = "UDP"
	}

	portText := strconv.Itoa(request.InternalPort)
	mapped, err := source.runUpnpc("-a", "", portText, portText, protocol)
	if err != nil {
		return 0, false
	}

	granted := request.InternalPort
	if match := upnpMapped.FindStringSubmatch(mapped); match != nil {
		if parsed, convErr := strconv.Atoi(match[1]); convErr == nil && parsed > 0 {
			granted = parsed
		}
	}

	return granted, true
}

func init() {
	source.Register("upnp", []config.OptionRule{
		{Name: "binary", Kind: "string"},
		{Name: "timeout", Kind: "int"},
	}, buildUpnpSource)
}

func buildUpnpSource(section config.Section, _ func(string)) (source.IPortSource, error) {
	binary, _ := section.String("binary")
	return &UpnpSource{
		Binary:  binary,
		Timeout: section.Int("timeout", 30),
	}, nil
}
