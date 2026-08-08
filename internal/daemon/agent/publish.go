package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sixxhands/fyrwire/internal/network/hostinfo"
	"github.com/sixxhands/fyrwire/internal/network/relay"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type AgentState struct {
	LastHost            string            `json:"last_host"`
	LastPorts           map[string]int    `json:"last_ports"`
	ConsecutiveFailures int               `json:"consecutive_failures"`
	TotalPublishes      int               `json:"total_publishes"`
	LastChangeAt        string            `json:"last_change_at"`
	LastError           string            `json:"last_error"`
	AlertedDown         bool              `json:"alerted_down"`
	AlertedServices     []string          `json:"alerted_services"`
	Published           map[string]string `json:"published"`
	LastVerified        int64             `json:"last_verified"`
}

func LoadState(path string) *AgentState {
	state := &AgentState{LastPorts: map[string]int{}, Published: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}

	if err := json.Unmarshal(data, state); err != nil {
		return &AgentState{LastPorts: map[string]int{}, Published: map[string]string{}}
	}

	if state.LastPorts == nil {
		state.LastPorts = map[string]int{}
	}
	if state.Published == nil {
		state.Published = map[string]string{}
	}

	return state
}

func (state *AgentState) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o600); err != nil {
		return err
	}

	return os.Rename(tempPath, path)
}

func aRecordKey(name, host string, ttl int) string {
	return fmt.Sprintf("%s|%s|%d", name, host, ttl)
}

func (agent *Agent) serviceRecordKey(service model.ServiceData, port int) string {
	ttl := service.Ttl
	if ttl == 0 {
		ttl = agent.plan.Ttl
	}

	return fmt.Sprintf("%s|%s|%d|%s|%d|%d|%d|%s",
		service.SrvName, agent.plan.AName, port, service.Protocol,
		service.Priority, service.Weight, ttl, service.Record)
}

func (agent *Agent) runPaced(write func() (string, error)) (string, error) {
	if !agent.lastWrite.IsZero() {
		if gap := time.Since(agent.lastWrite); gap < WriteSpacing {
			time.Sleep(WriteSpacing - gap)
		}
	}

	defer func() { agent.lastWrite = time.Now() }()
	return write()
}

func collectSkipped(partial map[string]bool, relayFailed map[string]string) (
	map[string]bool, []string, map[string]string,
) {
	skip := map[string]bool{}
	reasons := map[string]string{}
	var skipped []string

	for key := range partial {
		skip[key] = true
		skipped = append(skipped, key)
		reasons[key] = "the gateway granted it no port"
	}

	for key, reason := range relayFailed {
		if !skip[key] {
			skipped = append(skipped, key)
		}
		skip[key] = true
		reasons[key] = reason
	}

	sort.Strings(skipped)
	return skip, skipped, reasons
}

func (agent *Agent) publishRecords(endpoints model.EndpointData, skip map[string]bool, force bool) ([]string, error) {
	var changes []string

	change, err := agent.publishAddressRecord(endpoints.Host, force)
	if err != nil {
		return nil, err
	}
	if change != "" {
		changes = append(changes, change)
	}

	for _, service := range agent.plan.Services {
		change, err := agent.publishService(service, endpoints, skip, force)
		if err != nil {
			return nil, err
		}
		if change != "" {
			changes = append(changes, change)
		}
	}

	return changes, nil
}

func (agent *Agent) publishAddressRecord(host string, force bool) (string, error) {
	key := aRecordKey(agent.plan.AName, host, agent.plan.Ttl)
	if !force && key == agent.State.Published["@a"] {
		return "", nil
	}

	change, err := agent.runPaced(func() (string, error) {
		return agent.provider.EnsureA(agent.plan.AName, host, agent.plan.Ttl)
	})
	if err != nil {
		return "", err
	}

	agent.State.LastHost = host
	agent.State.Published["@a"] = key
	return change, nil
}

func (agent *Agent) publishService(
	service model.ServiceData,
	endpoints model.EndpointData,
	skip map[string]bool,
	force bool,
) (string, error) {
	port, ok := endpoints.Ports[service.Key]
	if !ok || skip[service.Key] {
		return "", nil
	}

	key := agent.serviceRecordKey(service, port)
	if !force && agent.State.Published[service.Key] == key {
		return "", nil
	}

	if service.Record == model.RecordNone {
		agent.State.LastPorts[service.Key] = port
		agent.State.Published[service.Key] = key
		return "", nil
	}

	ttl := service.Ttl
	if ttl == 0 {
		ttl = agent.plan.Ttl
	}

	change, err := agent.ensureServiceRecord(service, port, ttl)
	if err != nil {
		return "", err
	}

	agent.State.LastPorts[service.Key] = port
	agent.State.Published[service.Key] = key
	return change, nil
}

func (agent *Agent) ensureServiceRecord(service model.ServiceData, port, ttl int) (string, error) {
	if service.Record == model.RecordHttps {
		record := model.HttpsRecordData{
			Name:     service.SrvName,
			Target:   agent.plan.AName,
			Port:     port,
			Priority: 1,
			Ttl:      ttl,
		}
		return agent.runPaced(func() (string, error) {
			return agent.provider.EnsureHttps(record)
		})
	}

	record := model.SrvRecordData{
		Name:     service.SrvName,
		Target:   agent.plan.AName,
		Port:     port,
		Priority: service.Priority,
		Weight:   service.Weight,
		Ttl:      ttl,
	}
	return agent.runPaced(func() (string, error) {
		return agent.provider.EnsureSrv(record)
	})
}

func (agent *Agent) syncRelays(endpoints model.EndpointData) ([]string, map[string]string) {
	failed := map[string]string{}
	if agent.relays == nil {
		return nil, failed
	}

	bindHost := agent.pickBindHost()
	if bindHost == "" {
		agent.options.Log("relay: no tunnel address found; skipping")
		return nil, failed
	}

	wanted := agent.buildWantedBindings(bindHost, endpoints)
	changes := agent.relays.Sync(wanted)
	live := agent.relays.Stats()

	for key, binding := range wanted {
		if _, ok := live[key]; !ok {
			failed[key] = fmt.Sprintf(
				"relay could not bind %s:%d (port in use or address not on this host)",
				binding.BindHost, binding.BindPort,
			)
		}
	}

	return changes, failed
}

func (agent *Agent) buildWantedBindings(bindHost string, endpoints model.EndpointData) map[string]relay.BindingData {
	wanted := map[string]relay.BindingData{}
	for _, service := range agent.plan.Services {
		port, ok := endpoints.Ports[service.Key]
		if !ok || (service.Protocol == "udp" && !agent.options.RelayUdp) {
			continue
		}

		wanted[service.Key] = relay.BindingData{
			BindHost:   bindHost,
			BindPort:   port,
			TargetHost: agent.options.TargetHost,
			TargetPort: service.InternalPort,
			Protocol:   service.Protocol,
		}
	}

	return wanted
}

func (agent *Agent) pickBindHost() string {
	if agent.options.BindHost != "" {
		return agent.options.BindHost
	}

	bindHost := ""
	if boundSource, ok := agent.source.(interface{ BindAddress() string }); ok {
		bindHost = boundSource.BindAddress()
	}

	egress := hostinfo.EgressAddress()
	if bindHost == "" {
		return egress
	}

	if egress != "" && egress != bindHost {
		agent.warnOnce(fmt.Sprintf("binding %s instead of %s (different routes on this host)", bindHost, egress))
	}

	return bindHost
}
