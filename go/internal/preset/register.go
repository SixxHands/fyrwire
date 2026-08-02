package preset

import "github.com/sixxhands/fyrwire/internal/config"

func registerLookup(fn func(string) (string, bool)) {
	config.PresetLookup = fn
}
