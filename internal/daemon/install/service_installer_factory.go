package install

import (
	"runtime"

	"github.com/sixxhands/fyrwire/internal/daemon/install/platforms"
)

type ServiceInstallerFactory struct{}

func (ServiceInstallerFactory) Create() IServiceInstaller {
	return ServiceInstallerFactory{}.CreateForPlatform(runtime.GOOS)
}

func (ServiceInstallerFactory) CreateForPlatform(goos string) IServiceInstaller {
	switch goos {
	case "linux":
		return platforms.SystemdInstaller{Launcher: Launcher, BuildWarning: BuildDirWarning}
	case "darwin":
		return platforms.LaunchdInstaller{Launcher: Launcher, BuildWarning: BuildDirWarning}
	case "windows":
		return platforms.WindowsInstaller{Launcher: Launcher, BuildWarning: BuildDirWarning}
	}

	return platforms.UnsupportedInstaller{Platform: goos}
}
