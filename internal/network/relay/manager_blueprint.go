package relay

import "github.com/sixxhands/fyrwire/internal/network/relay/listeners/udp"

type UdpOptionsData = udp.OptionsData

type ManagerBlueprint struct {
	log        func(string)
	udpOptions UdpOptionsData
}

func CreateManagerBlueprint(log func(string), options UdpOptionsData) ManagerBlueprint {
	if log == nil {
		log = func(string) {}
	}

	return ManagerBlueprint{log: log, udpOptions: options}
}
