package agent

import (
	"time"

	"github.com/sixxhands/fyrwire/internal/network/relay"
)

type AgentFactory struct{}

func (AgentFactory) Create(blueprint AgentBlueprint) *Agent {
	options := blueprint.options
	if options.Log == nil {
		options.Log = func(string) {}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.TargetHost == "" {
		options.TargetHost = "127.0.0.1"
	}

	agent := &Agent{
		source:    blueprint.source,
		provider:  blueprint.provider,
		plan:      blueprint.plan,
		statePath: blueprint.statePath,
		State:     LoadState(blueprint.statePath),
		options:   options,
		observers: append([]IAgentObserver(nil), options.Observers...),
	}

	if options.Relay {
		managerBlueprint := relay.CreateManagerBlueprint(options.Log, options.Udp)
		agent.relays = relay.ManagerFactory{}.Create(managerBlueprint)
	}

	return agent
}
