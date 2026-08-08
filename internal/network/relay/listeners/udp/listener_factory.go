package udp

type ListenerFactory struct{}

func (ListenerFactory) Create(blueprint ListenerBlueprint) *Listener {
	options := fillDefaultOptions(blueprint.options)
	return &Listener{
		key:        blueprint.key,
		bindAddr:   blueprint.bindAddr,
		targetAddr: blueprint.targetAddr,
		log:        blueprint.log,
		options:    options,
		done:       make(chan struct{}),
		sessions:   map[string]*udpSession{},
		perIp:      map[string]int{},
		tokens:     options.NewPerSec,
		tokensAt:   options.Now(),
	}
}
