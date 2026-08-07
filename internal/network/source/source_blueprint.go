package source

import (
	"github.com/sixxhands/fyrwire/internal/node/config"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type SourceBlueprint struct {
	kind    string
	section config.Section
	log     func(string)
}

func CreateSourceBlueprint(section config.Section, log func(string)) (SourceBlueprint, error) {
	kind := section.Type()
	if kind == "" {
		return SourceBlueprint{}, model.Errorf("source blueprint missing type")
	}

	if _, ok := registry[kind]; !ok {
		return SourceBlueprint{}, model.Errorf("unknown source type %q", kind)
	}

	copied := make(config.Section, len(section))
	for key, value := range section {
		copied[key] = value
	}

	return SourceBlueprint{kind: kind, section: copied, log: log}, nil
}
