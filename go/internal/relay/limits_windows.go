//go:build windows

package relay

func localPortRange() (int, int, bool) { return 0, 0, false }
func maxOpenFiles() (int, bool)        { return 0, false }
