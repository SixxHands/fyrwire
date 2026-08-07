package provider

import (
	"github.com/sixxhands/fyrwire/internal/node/config"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type ProviderBuilder func(section config.Section) (IDnsProvider, error)

var registry = map[string]ProviderBuilder{}

func Register(name string, options []config.OptionRule, build ProviderBuilder) {
	registry[name] = build
	config.RegisterProvider(name, options)
}

type ProviderFactory struct{}

func (ProviderFactory) Create(blueprint ProviderBlueprint) (IDnsProvider, error) {
	build, ok := registry[blueprint.kind]
	if !ok {
		return nil, model.Errorf("unknown provider type %q", blueprint.kind)
	}

	return build(blueprint.section)
}

func Available() []string {
	names := make([]string, 0, len(registry))
	for key := range registry {
		names = append(names, key)
	}

	return names
}
