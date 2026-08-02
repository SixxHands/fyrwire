package hostinfo

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const probeTarget = "1.1.1.1"

type InterfaceData struct {
	Name      string
	Address   string
	PrefixLen int
}

func (i InterfaceData) Tunnelish() bool {
	for _, p := range []string{"wg", "tun", "utun", "proton", "tap", "ppp"} {
		if strings.HasPrefix(i.Name, p) {
			return true
		}
	}
	return i.PrefixLen == 32
}

func LocalAddressToward(host string) string {
	conn, err := net.DialTimeout("udp4", net.JoinHostPort(host, "9"), 2*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	return addr.IP.String()
}

func EgressAddress() string { return LocalAddressToward(probeTarget) }

func SubnetRouter(address string) string {
	parts := strings.Split(address, ".")
	if len(parts) != 4 {
		return ""
	}
	return strings.Join(append(parts[:3:3], "1"), ".")
}

func Interfaces() []InterfaceData {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []InterfaceData
	for _, i := range ifaces {
		if i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			ones, _ := ipnet.Mask.Size()
			out = append(out, InterfaceData{Name: i.Name, Address: ipnet.IP.String(),
				PrefixLen: ones})
		}
	}
	return out
}

type Port struct {
	Port    int
	Process string
}

type IHostBackend interface {
	Name() string

	Verified() bool
	ListeningPorts() ([]Port, error)

	Health() (bool, string)
}

func Current() IHostBackend {
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

type linuxBackend struct{}

func (linuxBackend) Name() string   { return "linux" }
func (linuxBackend) Verified() bool { return true }

func (l linuxBackend) ListeningPorts() ([]Port, error) {

	seen := map[int]bool{}
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Scan()
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 4 || fields[3] != "0A" {
				continue
			}
			local := fields[1]
			i := strings.LastIndex(local, ":")
			if i < 0 {
				continue
			}
			raw, err := hex.DecodeString(local[i+1:])
			if err != nil || len(raw) != 2 {
				continue
			}
			port := int(binary.BigEndian.Uint16(raw))
			if port > 1024 {
				seen[port] = true
			}
		}
		f.Close()
	}
	out := make([]Port, 0, len(seen))
	for p := range seen {
		out = append(out, Port{Port: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out, nil
}

func (l linuxBackend) Health() (bool, string) { return true, "" }

type macBackend struct{}

func (macBackend) Name() string   { return "macos" }
func (macBackend) Verified() bool { return false }

var portTail = regexp.MustCompile(`[.:](\d+)$`)

func (m macBackend) ListeningPorts() ([]Port, error) {
	seen := map[int]string{}

	if out := run("lsof", "-nP", "-iTCP", "-sTCP:LISTEN"); out != "" {
		for _, line := range strings.Split(out, "\n")[1:] {
			f := strings.Fields(line)
			if len(f) < 9 {
				continue
			}
			if mm := portTail.FindStringSubmatch(f[8]); mm != nil {
				if p, _ := strconv.Atoi(mm[1]); p > 1024 {
					if _, dup := seen[p]; !dup {
						seen[p] = f[0]
					}
				}
			}
		}
	}
	if len(seen) == 0 {
		for _, line := range strings.Split(run("netstat", "-anv", "-p", "tcp"), "\n") {
			if !strings.Contains(line, "LISTEN") {
				continue
			}
			if mm := regexp.MustCompile(`[.:](\d+)\s`).FindStringSubmatch(line); mm != nil {
				if p, _ := strconv.Atoi(mm[1]); p > 1024 {
					seen[p] = ""
				}
			}
		}
	}
	return sortPorts(seen), nil
}

func (m macBackend) Health() (bool, string) {
	if _, err := exec.LookPath("lsof"); err != nil {
		if _, err2 := exec.LookPath("netstat"); err2 != nil {
			return false, "cannot list listening ports: neither lsof nor netstat found"
		}
	}
	return true, unverifiedNote("macos")
}

type windowsBackend struct{}

func (windowsBackend) Name() string   { return "windows" }
func (windowsBackend) Verified() bool { return false }

func (w windowsBackend) ListeningPorts() ([]Port, error) {
	seen := map[int]string{}
	for _, line := range strings.Split(run("netstat", "-ano", "-p", "TCP"), "\n") {
		if !strings.Contains(line, "LISTENING") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if mm := portTail.FindStringSubmatch(f[1]); mm != nil {
			if p, _ := strconv.Atoi(mm[1]); p > 1024 {
				seen[p] = ""
			}
		}
	}
	return sortPorts(seen), nil
}

func (w windowsBackend) Health() (bool, string) {
	if _, err := exec.LookPath("netstat"); err != nil {
		return false, "cannot list listening ports: netstat not found on PATH"
	}
	return true, unverifiedNote("windows")
}

type fallbackBackend struct{}

func (fallbackBackend) Name() string   { return "unknown" }
func (fallbackBackend) Verified() bool { return false }

func (fallbackBackend) ListeningPorts() ([]Port, error) { return nil, nil }

func (fallbackBackend) Health() (bool, string) {
	return false, "no listening-port backend for " + runtime.GOOS +
		". Everything else still works — port detection is a convenience, not " +
		"a dependency — so enter ports manually and carry on."
}

func unverifiedNote(os string) string {
	return "the " + os + " backend has never been run on " + os + " by its " +
		"authors — it is written from documented output formats and may be wrong"
}

func run(name string, args ...string) string {
	if _, err := exec.LookPath(name); err != nil {
		return ""
	}
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return string(out)
	}
	return string(out)
}

func sortPorts(seen map[int]string) []Port {
	out := make([]Port, 0, len(seen))
	for p, proc := range seen {
		out = append(out, Port{Port: p, Process: proc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}
