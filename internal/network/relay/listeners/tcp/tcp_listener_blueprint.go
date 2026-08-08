package tcp

import "errors"

type ListenerBlueprint struct {
	key        string
	bindAddr   string
	targetAddr string
	log        func(string)
}

func CreateListenerBlueprint(key, bindAddr, targetAddr string, log func(string)) (ListenerBlueprint, error) {
	if bindAddr == "" {
		return ListenerBlueprint{}, errors.New("tcp listener blueprint missing bindAddr")
	}

	if targetAddr == "" {
		return ListenerBlueprint{}, errors.New("tcp listener blueprint missing targetAddr")
	}

	if log == nil {
		log = func(string) {}
	}

	return ListenerBlueprint{
		key:        key,
		bindAddr:   bindAddr,
		targetAddr: targetAddr,
		log:        log,
	}, nil
}
