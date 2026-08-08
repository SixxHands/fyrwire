package platforms

import "github.com/sixxhands/fyrwire/internal/node/model"

type UnsupportedInstaller struct {
	Platform string
}

func (installer UnsupportedInstaller) Install(_ string, _ func(string)) error {
	return model.Errorf(
		"no service installer for %s; start `fyrwire run` with your system service manager",
		installer.Platform,
	)
}

func (installer UnsupportedInstaller) Uninstall(_ func(string)) error {
	return model.Errorf("nothing to uninstall on %s", installer.Platform)
}
