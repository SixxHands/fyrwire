package main

import (
	"github.com/sixxhands/fyrwire/internal/daemon/agent"
	"github.com/sixxhands/fyrwire/internal/daemon/notify"
)

type alertObserver struct {
	notifier *notify.Notifier
	nodeName string
}

func (observer alertObserver) OnTick(result agent.TickResult, state *agent.AgentState) {
	if observer.notifier == nil {
		return
	}

	alertState := &notify.AlertState{
		Down:     state.AlertedDown,
		Services: state.AlertedServices,
	}

	errorText := ""
	if result.Err != nil {
		errorText = result.Err.Error()
	}

	status := notify.AlertStatusData{
		Ok:       result.Ok,
		Host:     result.Host,
		Error:    errorText,
		Skipped:  result.Skipped,
		Reasons:  result.Reasons,
		Failures: state.ConsecutiveFailures,
		KeptPort: state.LastPorts,
	}

	observer.notifier.CheckTransitions(status, alertState, observer.nodeName)
	state.AlertedDown = alertState.Down
	state.AlertedServices = alertState.Services
}
