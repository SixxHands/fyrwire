package source

import (
	"github.com/sixxhands/fyrwire/internal/node/config"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type SourceBuilder func(section config.Section, log func(string)) (IPortSource, error)

var registry = map[string]SourceBuilder{}

func Register(name string, options []config.OptionRule, build SourceBuilder) {
	registry[name] = build
	config.RegisterSource(name, options)
}

type SourceFactory struct{}

func (SourceFactory) Create(blueprint SourceBlueprint) (IPortSource, error) {
	build, ok := registry[blueprint.kind]
	if !ok {
		return nil, model.Errorf("unknown source type %q", blueprint.kind)
	}

	return build(blueprint.section, blueprint.log)
}

func Available() []string {
	names := make([]string, 0, len(registry))
	for key := range registry {
		names = append(names, key)
	}

	return names
}
