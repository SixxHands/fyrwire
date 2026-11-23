package hostinfo

import (
	"net"
	"runtime"
	"strings"
	"testing"
)

func Test_local_address_toward_answers_without_sending_packets(t *testing.T) {
	addr := LocalAddressToward("1.1.1.1")
	if addr == "" {
		t.Skip("no route to the internet in this environment")
	}
	if net.ParseIP(addr) == nil {
		t.Errorf("not an address: %q", addr)
	}
}

func Test_subnet_router_takes_the_dot_one(t *testing.T) {
	if got := SubnetRouter("10.2.0.57"); got != "10.2.0.1" {
		t.Errorf("got %q", got)
	}
	if got := SubnetRouter("nonsense"); got != "" {
		t.Errorf("bad input must yield empty, got %q", got)
	}
}

func Test_subnet_router_does_not_corrupt_its_input(t *testing.T) {
	addr := "192.168.1.42"
	_ = SubnetRouter(addr)
	if addr != "192.168.1.42" {
		t.Errorf("input mutated to %q", addr)
	}
}

func Test_is_tunnel_recognizes_vpn_interfaces(t *testing.T) {
	for _, c := range []struct {
		iface InterfaceData
		want  bool
	}{
		{InterfaceData{Name: "wg0"}, true},
		{InterfaceData{Name: "proton0"}, true},
		{InterfaceData{Name: "utun3"}, true},
		{InterfaceData{Name: "eth0", PrefixLen: 24}, false},
		{InterfaceData{Name: "nordlynx", PrefixLen: 32}, true},
	} {
		if got := c.iface.IsTunnel(); got != c.want {
			t.Errorf("%+v: got %v want %v", c.iface, got, c.want)
		}
	}
}

func Test_interfaces_enumerates_without_shelling_out(t *testing.T) {
	got := Interfaces()
	if len(got) == 0 {
		t.Skip("no non-loopback IPv4 interfaces here")
	}
	for _, i := range got {
		if net.ParseIP(i.Address) == nil {
			t.Errorf("%+v has a bad address", i)
		}
		if strings.HasPrefix(i.Name, "lo") {
			t.Errorf("loopback must be excluded: %+v", i)
		}
	}
}

func Test_only_linux_claims_to_be_verified(t *testing.T) {
	if !(linuxBackend{}).Verified() {
		t.Error("linux backend should be marked verified")
	}
	for _, b := range []IHostBackend{macBackend{}, windowsBackend{}, fallbackBackend{}} {
		if b.Verified() {
			t.Errorf("%s should not claim to be verified", b.Name())
		}
	}
}

func Test_untested_backends_report_note(t *testing.T) {
	ok, why := macBackend{}.Health()
	if !ok {
		t.Skip("lsof and netstat both absent here")
	}
	if !strings.Contains(why, "untested") {
		t.Errorf("health note = %q", why)
	}
}

func Test_unknown_platform_explains_what_still_works(t *testing.T) {
	ok, why := fallbackBackend{}.Health()
	if ok {
		t.Error("an unknown platform cannot list ports")
	}
	if !strings.Contains(why, "manually") {
		t.Errorf("message = %q", why)
	}
}

func Test_linux_finds_a_port_we_just_opened(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	want := ln.Addr().(*net.TCPAddr).Port
	ports, err := linuxBackend{}.ListeningPorts()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ports {
		if p.Port == want {
			return
		}
	}
	t.Errorf("did not find port %d among %d listeners", want, len(ports))
}
