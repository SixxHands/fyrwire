//go:build !windows

package relay

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func localPortRange() (lo, hi int, ok bool) {
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 0, 0, false
	}
	f := strings.Fields(string(data))
	if len(f) != 2 {
		return 0, 0, false
	}
	lo, err1 := strconv.Atoi(f[0])
	hi, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || hi <= lo {
		return 0, 0, false
	}
	return lo, hi, true
}

func maxOpenFiles() (int, bool) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return 0, false
	}
	if lim.Cur == 0 {
		return 0, false
	}
	return int(lim.Cur), true
}
