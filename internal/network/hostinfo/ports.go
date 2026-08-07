package hostinfo

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

var (
	portTail       = regexp.MustCompile(`[.:](\d+)$`)
	netstatMacPort = regexp.MustCompile(`[.:](\d+)\s`)
)

type linuxBackend struct{}

func (linuxBackend) Name() string           { return "linux" }
func (linuxBackend) Verified() bool         { return true }
func (linuxBackend) Health() (bool, string) { return true, "" }

func (linuxBackend) ListeningPorts() ([]PortData, error) {
	seen := map[int]bool{}
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		readProcTcp(path, seen)
	}
	out := make([]PortData, 0, len(seen))
	for p := range seen {
		out = append(out, PortData{Port: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out, nil
}

func readProcTcp(path string, seen map[int]bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Scan()
	for scanner.Scan() {
		if port, ok := readProcListenPort(scanner.Text()); ok {
			seen[port] = true
		}
	}
}

func readProcListenPort(line string) (int, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 || fields[3] != "0A" {
		return 0, false
	}
	local := fields[1]
	i := strings.LastIndex(local, ":")
	if i < 0 {
		return 0, false
	}
	raw, err := hex.DecodeString(local[i+1:])
	if err != nil || len(raw) != 2 {
		return 0, false
	}
	port := int(binary.BigEndian.Uint16(raw))
	return port, port > 1024
}

type macBackend struct{}

func (macBackend) Name() string   { return "macos" }
func (macBackend) Verified() bool { return false }

func (macBackend) ListeningPorts() ([]PortData, error) {
	seen := map[int]string{}
	readMacLsof(seen)
	if len(seen) == 0 {
		readMacNetstat(seen)
	}
	return sortPorts(seen), nil
}

func readMacLsof(seen map[int]string) {
	out := runCommand("lsof", "-nP", "-iTCP", "-sTCP:LISTEN")
	if out == "" {
		return
	}
	for _, line := range strings.Split(out, "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 9 {
			continue
		}
		mm := portTail.FindStringSubmatch(f[8])
		if mm == nil {
			continue
		}
		if p, _ := strconv.Atoi(mm[1]); p > 1024 {
			if _, dup := seen[p]; !dup {
				seen[p] = f[0]
			}
		}
	}
}

func readMacNetstat(seen map[int]string) {
	for _, line := range strings.Split(runCommand("netstat", "-anv", "-p", "tcp"), "\n") {
		if !strings.Contains(line, "LISTEN") {
			continue
		}
		mm := netstatMacPort.FindStringSubmatch(line)
		if mm == nil {
			continue
		}
		if p, _ := strconv.Atoi(mm[1]); p > 1024 {
			seen[p] = ""
		}
	}
}

func (macBackend) Health() (bool, string) {
	if _, err := exec.LookPath("lsof"); err != nil {
		if _, err2 := exec.LookPath("netstat"); err2 != nil {
			return false, "cannot list listening ports: neither lsof nor netstat found"
		}
	}
	return true, untestedNote("macos")
}

type windowsBackend struct{}

func (windowsBackend) Name() string   { return "windows" }
func (windowsBackend) Verified() bool { return false }

func (windowsBackend) ListeningPorts() ([]PortData, error) {
	seen := map[int]string{}
	for _, line := range strings.Split(runCommand("netstat", "-ano", "-p", "TCP"), "\n") {
		if !strings.Contains(line, "LISTENING") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		mm := portTail.FindStringSubmatch(f[1])
		if mm == nil {
			continue
		}
		if p, _ := strconv.Atoi(mm[1]); p > 1024 {
			seen[p] = ""
		}
	}
	return sortPorts(seen), nil
}

func (windowsBackend) Health() (bool, string) {
	if _, err := exec.LookPath("netstat"); err != nil {
		return false, "cannot list listening ports: netstat not found on PATH"
	}
	return true, untestedNote("windows")
}

type fallbackBackend struct{}

func (fallbackBackend) Name() string                        { return "unknown" }
func (fallbackBackend) Verified() bool                      { return false }
func (fallbackBackend) ListeningPorts() ([]PortData, error) { return nil, nil }

func (fallbackBackend) Health() (bool, string) {
	return false, "no listening-port backend for " + runtime.GOOS + "; enter ports manually instead"
}

func untestedNote(osName string) string {
	return "port detection on " + osName + " is untested and may miss listeners"
}

func runCommand(name string, args ...string) string {
	if _, err := exec.LookPath(name); err != nil {
		return ""
	}
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return string(out)
	}
	return string(out)
}

func sortPorts(seen map[int]string) []PortData {
	out := make([]PortData, 0, len(seen))
	for p, proc := range seen {
		out = append(out, PortData{Port: p, Process: proc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}
