package manual

import (
	"github.com/sixxhands/fyrwire/internal/network/source"
	"github.com/sixxhands/fyrwire/internal/node/config"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type StaticSource struct {
	Host  string
	Ports map[string]int
}

func (item *StaticSource) Name() string {
	return "static"
}

func (item *StaticSource) FindEndpoints(requests []model.PortRequestData) (model.EndpointData, error) {
	endpoints := model.EndpointData{
		Host:    item.Host,
		Ports:   map[string]int{},
		Partial: map[string]bool{},
	}

	for _, request := range requests {
		if port, ok := item.Ports[request.Key]; ok {
			endpoints.Ports[request.Key] = port
		} else {
			endpoints.Ports[request.Key] = request.InternalPort
		}
	}

	return endpoints, nil
}

func init() {
	source.Register("static", []config.OptionRule{
		{Name: "host", Required: true, Kind: "string"},
		{Name: "ports", Kind: "table"},
	}, buildStaticSource)
}

func buildStaticSource(section config.Section, _ func(string)) (source.IPortSource, error) {
	host, err := section.String("host")
	if err != nil {
		return nil, err
	}

	ports := map[string]int{}
	if raw, ok := section["ports"].(map[string]any); ok {
		for key, value := range raw {
			if port, ok := value.(int64); ok {
				ports[key] = int(port)
			}
		}
	}

	return &StaticSource{Host: host, Ports: ports}, nil
}
