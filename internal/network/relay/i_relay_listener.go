package relay

import (
	"net"
	"strconv"

	"github.com/sixxhands/fyrwire/internal/node/model"
)

type IRelayListener interface {
	Start() error
	Stop()
	Stats() map[string]any
}

type BindingData struct {
	BindHost   string
	BindPort   int
	TargetHost string
	TargetPort int
	Protocol   string
}

func CreateBindingBlueprint(bindHost string, bindPort int, targetHost string, targetPort int, protocol string) (BindingData, error) {
	if bindHost == "" {
		return BindingData{}, model.Errorf("relay binding missing bindHost")
	}
	if targetHost == "" {
		return BindingData{}, model.Errorf("relay binding missing targetHost")
	}
	if bindPort < 1 || bindPort > 65535 {
		return BindingData{}, model.Errorf("relay binding bindPort %d out of range", bindPort)
	}
	if targetPort < 1 || targetPort > 65535 {
		return BindingData{}, model.Errorf("relay binding targetPort %d out of range", targetPort)
	}

	if protocol == "" {
		protocol = "tcp"
	}
	if protocol != "tcp" && protocol != "udp" {
		return BindingData{}, model.Errorf("relay binding unsupported protocol %q", protocol)
	}

	return BindingData{
		BindHost:   bindHost,
		BindPort:   bindPort,
		TargetHost: targetHost,
		TargetPort: targetPort,
		Protocol:   protocol,
	}, nil
}

func (binding BindingData) bindAddr() string {
	return net.JoinHostPort(binding.BindHost, strconv.Itoa(binding.BindPort))
}

func (binding BindingData) targetAddr() string {
	return net.JoinHostPort(binding.TargetHost, strconv.Itoa(binding.TargetPort))
}
