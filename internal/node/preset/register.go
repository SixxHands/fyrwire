package preset

import "github.com/sixxhands/fyrwire/internal/node/config"

func registerLookup(fn func(key string) (record, protocol string, ok bool)) {
	config.PresetLookup = fn
}
