package hostinfo

type IHostBackend interface {
	Name() string
	Verified() bool
	ListeningPorts() ([]PortData, error)
	Health() (bool, string)
}
