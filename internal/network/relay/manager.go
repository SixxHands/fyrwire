package relay

import (
	"fmt"
	"sync"

	"github.com/sixxhands/fyrwire/internal/network/relay/listeners/tcp"
	"github.com/sixxhands/fyrwire/internal/network/relay/listeners/udp"
)

type Manager struct {
	mu         sync.Mutex
	listeners  map[string]IRelayListener
	bindings   map[string]BindingData
	log        func(string)
	udpOptions UdpOptionsData
}

func (manager *Manager) Sync(wanted map[string]BindingData) []string {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	changes := manager.stopRemoved(wanted)
	for key, binding := range wanted {
		if message := manager.syncOne(key, binding); message != "" {
			changes = append(changes, message)
		}
	}

	return changes
}

func (manager *Manager) stopRemoved(wanted map[string]BindingData) []string {
	var changes []string
	for key, listener := range manager.listeners {
		if _, keep := wanted[key]; !keep {
			listener.Stop()
			delete(manager.listeners, key)
			delete(manager.bindings, key)
			changes = append(changes, "relay "+key+" stopped")
		}
	}

	return changes
}

func (manager *Manager) syncOne(key string, binding BindingData) string {
	if binding.Protocol == "" {
		binding.Protocol = "tcp"
	}

	wasBound := manager.stopIfChanged(key, binding)
	if !wasBound {
		if _, unchanged := manager.listeners[key]; unchanged {
			return ""
		}
	}

	listener, err := manager.buildListener(key, binding)
	if err != nil {
		return fmt.Sprintf("relay %s FAILED to bind %s/%s: %v",
			key, binding.bindAddr(), binding.Protocol, err)
	}

	if err := listener.Start(); err != nil {
		return fmt.Sprintf("relay %s FAILED to bind %s/%s: %v",
			key, binding.bindAddr(), binding.Protocol, err)
	}

	manager.listeners[key] = listener
	manager.bindings[key] = binding

	verb := "listening"
	if wasBound {
		verb = "rebound"
	}

	return fmt.Sprintf("relay %s %s %s -> %s (%s)",
		key, verb, binding.bindAddr(), binding.targetAddr(), binding.Protocol)
}

func (manager *Manager) stopIfChanged(key string, binding BindingData) bool {
	current, exists := manager.listeners[key]
	if !exists || manager.bindings[key] == binding {
		return false
	}

	current.Stop()
	delete(manager.listeners, key)
	delete(manager.bindings, key)
	return true
}

func (manager *Manager) buildListener(key string, binding BindingData) (IRelayListener, error) {
	if binding.Protocol == "udp" {
		blueprint, err := udp.CreateListenerBlueprint(
			key,
			binding.bindAddr(),
			binding.targetAddr(),
			manager.log,
			manager.udpOptions,
		)
		if err != nil {
			return nil, err
		}

		return udp.ListenerFactory{}.Create(blueprint), nil
	}

	blueprint, err := tcp.CreateListenerBlueprint(
		key,
		binding.bindAddr(),
		binding.targetAddr(),
		manager.log,
	)
	if err != nil {
		return nil, err
	}

	return tcp.ListenerFactory{}.Create(blueprint), nil
}

func (manager *Manager) Stats() map[string]map[string]any {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	stats := map[string]map[string]any{}
	for key, listener := range manager.listeners {
		stats[key] = listener.Stats()
	}

	return stats
}

func (manager *Manager) Stop() {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	for key, listener := range manager.listeners {
		listener.Stop()
		delete(manager.listeners, key)
	}
}
