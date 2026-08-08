package tcp

import "net"

type ListenerFactory struct{}

func (ListenerFactory) Create(blueprint ListenerBlueprint) *Listener {
	return &Listener{
		key:        blueprint.key,
		bindAddr:   blueprint.bindAddr,
		targetAddr: blueprint.targetAddr,
		log:        blueprint.log,
		done:       make(chan struct{}),
		sem:        make(chan struct{}, tcpMaxConns),
		connTime:   tcpConnectTimeout,
		conns:      map[net.Conn]struct{}{},
	}
}
