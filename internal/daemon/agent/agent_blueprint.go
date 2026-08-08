package agent

import (
	"time"

	"github.com/sixxhands/fyrwire/internal/network/provider"
	"github.com/sixxhands/fyrwire/internal/network/relay"
	"github.com/sixxhands/fyrwire/internal/network/source"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type OptionsData struct {
	Relay      bool
	RelayUdp   bool
	BindHost   string
	TargetHost string
	Udp        relay.UdpOptionsData
	Log        func(string)
	Now        func() time.Time
	Observers  []IAgentObserver
	NodeName   string
}

type AgentBlueprint struct {
	source    source.IPortSource
	provider  provider.IDnsProvider
	plan      model.NodePlanData
	statePath string
	options   OptionsData
}

func CreateAgentBlueprint(
	portSource source.IPortSource,
	dnsProvider provider.IDnsProvider,
	plan model.NodePlanData,
	statePath string,
	options OptionsData,
) (AgentBlueprint, error) {
	if portSource == nil {
		return AgentBlueprint{}, model.Errorf("agent blueprint missing source")
	}
	if dnsProvider == nil {
		return AgentBlueprint{}, model.Errorf("agent blueprint missing provider")
	}
	if plan.AName == "" {
		return AgentBlueprint{}, model.Errorf("agent blueprint missing node a_name")
	}

	options.Observers = append([]IAgentObserver(nil), options.Observers...)
	return AgentBlueprint{
		source:    portSource,
		provider:  dnsProvider,
		plan:      plan,
		statePath: statePath,
		options:   options,
	}, nil
}
