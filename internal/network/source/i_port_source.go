package source

import "github.com/sixxhands/fyrwire/internal/node/model"

type IPortSource interface {
	Name() string
	FindEndpoints(requests []model.PortRequestData) (model.EndpointData, error)
}
