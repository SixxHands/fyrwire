//go:build windows

package limits

func LocalPortRange() (int, int, bool) { return 0, 0, false }
func MaxOpenFiles() (int, bool)        { return 0, false }
