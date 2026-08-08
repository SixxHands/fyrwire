package relay

type ManagerFactory struct{}

func (ManagerFactory) Create(blueprint ManagerBlueprint) *Manager {
	logFunction := blueprint.log
	if logFunction == nil {
		logFunction = func(string) {}
	}

	return &Manager{
		listeners:  map[string]IRelayListener{},
		bindings:   map[string]BindingData{},
		log:        logFunction,
		udpOptions: blueprint.udpOptions,
	}
}
