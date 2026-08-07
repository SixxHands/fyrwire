package provider

import (
	"github.com/sixxhands/fyrwire/internal/node/config"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type ProviderBlueprint struct {
	kind    string
	section config.Section
}

func CreateProviderBlueprint(section config.Section) (ProviderBlueprint, error) {
	kind := section.Type()
	if kind == "" {
		return ProviderBlueprint{}, model.Errorf("provider blueprint missing type")
	}

	if _, ok := registry[kind]; !ok {
		return ProviderBlueprint{}, model.Errorf("unknown provider type %q", kind)
	}

	copied := make(config.Section, len(section))
	for key, value := range section {
		copied[key] = value
	}

	return ProviderBlueprint{kind: kind, section: copied}, nil
}
