//go:build !windows

package limits

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func LocalPortRange() (low, high int, ok bool) {
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 0, 0, false
	}

	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return 0, 0, false
	}

	low, err1 := strconv.Atoi(fields[0])
	high, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil || high <= low {
		return 0, 0, false
	}

	return low, high, true
}

func MaxOpenFiles() (int, bool) {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return 0, false
	}

	if limit.Cur == 0 {
		return 0, false
	}

	return int(limit.Cur), true
}
