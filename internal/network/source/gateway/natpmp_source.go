package gateway

import (
	"fmt"
	"net"

	"github.com/sixxhands/fyrwire/internal/network/hostinfo"
	"github.com/sixxhands/fyrwire/internal/network/source"
	"github.com/sixxhands/fyrwire/internal/node/config"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type NatPmpSource struct {
	Gateway   string
	Lifetime  int
	Timeout   float64
	Retries   int
	Log       func(string)
	Restarted bool

	lastEpoch uint32
	granted   int
	warned    map[string]bool
}

func (source *NatPmpSource) Name() string {
	return "natpmp"
}

func (source *NatPmpSource) warnOnce(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if source.warned == nil {
		source.warned = map[string]bool{}
	}

	if source.warned[message] {
		return
	}

	source.warned[message] = true
	if source.Log != nil {
		source.Log(message)
	}
}

func (source *NatPmpSource) PublicAddress() (string, error) {
	reply, err := source.exchange([]byte{natpmpVersion, opPublicAddr}, 12)
	if err != nil {
		return "", err
	}

	if err := checkNatPmpResult(reply, opPublicAddr); err != nil {
		return "", err
	}

	address := net.IP(reply[8:12]).String()
	if reason := checkUnroutableAddress(address); reason != "" {
		source.warnOnce("gateway reported %s as public address, but %s", address, reason)
	}

	return address, nil
}

// If the gateway epoch goes backwards, the router or VPN restarted and forgot all our mappings :(
func (source *NatPmpSource) checkEpoch(epoch uint32) {
	previous := source.lastEpoch
	source.lastEpoch = epoch
	if previous != 0 && epoch < previous {
		source.Restarted = true
		if source.Log != nil {
			source.Log(fmt.Sprintf("gateway restarted (epoch %d -> %d); recreating mappings", previous, epoch))
		}
	}
}

func (source *NatPmpSource) TookRestart() bool {
	wasRestarted := source.Restarted
	source.Restarted = false
	return wasRestarted
}

func (source *NatPmpSource) BindAddress() string {
	return hostinfo.LocalAddressToward(source.Gateway)
}

func (source *NatPmpSource) GrantedLifetime() int {
	return source.granted
}

func (source *NatPmpSource) noteLifetime(seconds int) {
	if seconds <= 0 {
		return
	}

	if source.granted == 0 || seconds < source.granted {
		source.granted = seconds
	}

	if source.Lifetime > 0 && seconds < source.Lifetime {
		source.warnOnce("requested %ds lease, gateway granted %ds", source.Lifetime, seconds)
	}
}

// Some VPNs (looking at you, ProtonVPN smh) hand out one port per session and ignore whatever internal port you asked for
func (source *NatPmpSource) SinglePort(protocol string) (int, bool) {
	gotInternal, external, granted, err := source.mapRawFull(0, protocol)
	if err != nil {
		return 0, false
	}

	source.noteLifetime(granted)
	if gotInternal != 0 && gotInternal == external && external > 0 {
		return external, true // annoying
	}

	return 0, false
}

func (source *NatPmpSource) Map(internalPort int, protocol string) (int, error) {
	if internalPort < 1 || internalPort > 65535 {
		return 0, model.Errorf("internal port %d is out of range (1..65535)", internalPort)
	}

	gotInternal, granted, lease, err := source.mapRawFull(internalPort, protocol)
	if err != nil {
		return 0, err
	}

	source.noteLifetime(lease)
	if gotInternal != internalPort {
		return 0, model.Errorf(
			"asked to map internal port %d but gateway answered with internal port %d",
			internalPort, gotInternal,
		)
	}

	if granted == 0 {
		return 0, model.Errorf("gateway returned success with port 0")
	}

	return granted, nil
}

func (source *NatPmpSource) FindEndpoints(requests []model.PortRequestData) (model.EndpointData, error) {
	host, err := source.PublicAddress()
	if err != nil {
		return model.EndpointData{}, err
	}

	endpoints := model.EndpointData{
		Host:    host,
		Ports:   map[string]int{},
		Partial: map[string]bool{},
	}

	if len(requests) > 0 && source.fillSinglePort(requests, &endpoints) {
		return endpoints, nil
	}

	for _, request := range requests {
		port, err := source.Map(request.InternalPort, request.Protocol)
		if err != nil {
			endpoints.Partial[request.Key] = true
			continue
		}
		endpoints.Ports[request.Key] = port
	}

	if len(endpoints.Ports) == 0 && len(requests) > 0 {
		return model.EndpointData{}, model.Errorf(
			"gateway granted no ports; check that port forwarding is enabled")
	}

	return endpoints, nil
}

func (source *NatPmpSource) fillSinglePort(requests []model.PortRequestData, endpoints *model.EndpointData) bool {
	protocols := map[string]bool{}
	for _, request := range requests {
		protocols[request.Protocol] = true
	}

	grantedByProtocol := map[string]int{}
	for protocol := range protocols {
		if port, single := source.SinglePort(protocol); single {
			grantedByProtocol[protocol] = port
		}
	}

	if len(grantedByProtocol) == 0 {
		return false
	}

	for _, request := range requests {
		port, ok := grantedByProtocol[request.Protocol]
		if !ok {
			endpoints.Partial[request.Key] = true
			continue
		}
		endpoints.Ports[request.Key] = port
	}

	if len(protocols) > 1 {
		source.warnOnce("gateway grants one port per protocol; tcp and udp do not share a request")
	} // nucance of a VPN alert
	if len(requests) > len(protocols) {
		source.warnOnce("gateway grants one port per session; services sharing a protocol share that port")
	}

	return true
}

func init() {
	source.Register("natpmp", []config.OptionRule{
		{Name: "gateway", Required: true, Kind: "string"},
		{Name: "lifetime", Kind: "int"},
		{Name: "timeout", Kind: "float"},
		{Name: "retries", Kind: "int"},
	}, buildNatPmpSource)
}

func buildNatPmpSource(section config.Section, log func(string)) (source.IPortSource, error) {
	gateway, err := section.String("gateway")
	if err != nil {
		return nil, err
	}

	return &NatPmpSource{
		Gateway:  gateway,
		Lifetime: section.Int("lifetime", 120),
		Timeout:  section.Float("timeout", 3),
		Retries:  section.Int("retries", 3),
		Log:      log,
	}, nil
}
