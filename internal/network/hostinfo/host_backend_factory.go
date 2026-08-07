package hostinfo

import "runtime"

type HostBackendFactory struct{}

func (HostBackendFactory) Create() IHostBackend {
	switch runtime.GOOS {
	case "linux":
		return linuxBackend{}
	case "darwin":
		return macBackend{}
	case "windows":
		return windowsBackend{}
	}

	return fallbackBackend{}
}
