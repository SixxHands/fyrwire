package udp

import (
	"errors"
	"time"

	"github.com/sixxhands/fyrwire/internal/network/relay/listeners/udp/limits"
)

type OptionsData struct {
	IdleNew            time.Duration
	IdleEstablished    time.Duration
	MaxSessions        int
	MaxPerIp           int
	NewPerSec          float64
	ReapInterval       time.Duration
	DropZeroLength     bool
	MaxSessionsSet     bool
	MaxPerIpSet        bool
	NewPerSecSet       bool
	IdleNewSet         bool
	IdleEstablishedSet bool
	Now                func() time.Time
}

// Force the session cap to what the OS actually lets us open.
func clampSessions(limit int) int {
	if limit <= 0 {
		return limit
	}

	if low, high, ok := limits.LocalPortRange(); ok && high-low < limit {
		limit = high - low
	}

	if openLimit, ok := limits.MaxOpenFiles(); ok {
		if room := openLimit - 128; room > 16 && room < limit {
			limit = room // sure, whatever value it is
		}
	}

	if limit < 1 {
		return 1
	}

	return limit
}

func fillDefaultOptions(options OptionsData) OptionsData {
	if options.IdleNew == 0 && !options.IdleNewSet {
		options.IdleNew = 30 * time.Second
	}
	if options.IdleEstablished == 0 && !options.IdleEstablishedSet {
		options.IdleEstablished = 180 * time.Second
	}
	if options.MaxSessions == 0 && !options.MaxSessionsSet {
		options.MaxSessions = 512
	}
	options.MaxSessions = clampSessions(options.MaxSessions)

	if options.MaxPerIp == 0 && !options.MaxPerIpSet {
		options.MaxPerIp = 8
	}
	if options.NewPerSec == 0 && !options.NewPerSecSet {
		options.NewPerSec = 32
	}
	if options.ReapInterval == 0 {
		options.ReapInterval = 5 * time.Second
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	return options
}

type ListenerBlueprint struct {
	key        string
	bindAddr   string
	targetAddr string
	log        func(string)
	options    OptionsData
}

func CreateListenerBlueprint(key, bindAddr, targetAddr string, log func(string), options OptionsData) (ListenerBlueprint, error) {
	if bindAddr == "" {
		return ListenerBlueprint{}, errors.New("udp listener blueprint missing bindAddr")
	}

	if targetAddr == "" {
		return ListenerBlueprint{}, errors.New("udp listener blueprint missing targetAddr")
	}

	return ListenerBlueprint{
		key:        key,
		bindAddr:   bindAddr,
		targetAddr: targetAddr,
		log:        log,
		options:    options,
	}, nil
}
