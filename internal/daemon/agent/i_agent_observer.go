package agent

type IAgentObserver interface {
	OnTick(result TickResult, state *AgentState)
}
