package hostinfo

import (
	"net"
	"runtime"
	"strings"
	"testing"
)

func TestLocalAddressTowardAnswersWithoutSendingPackets(t *testing.T) {
	addr := LocalAddressToward("1.1.1.1")
	if addr == "" {
		t.Skip("no route to the internet in this environment")
	}
	if net.ParseIP(addr) == nil {
		t.Errorf("not an address: %q", addr)
	}
}

func TestSubnetRouterTakesTheDotOne(t *testing.T) {
	if got := SubnetRouter("10.2.0.57"); got != "10.2.0.1" {
		t.Errorf("got %q", got)
	}
	if got := SubnetRouter("nonsense"); got != "" {
		t.Errorf("bad input must yield empty, got %q", got)
	}
}

func TestSubnetRouterDoesNotCorruptItsInput(t *testing.T) {
	addr := "192.168.1.42"
	_ = SubnetRouter(addr)
	if addr != "192.168.1.42" {
		t.Errorf("input mutated to %q", addr)
	}
}

func TestTunnelishRecognisesVPNInterfaces(t *testing.T) {
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
		if got := c.iface.Tunnelish(); got != c.want {
			t.Errorf("%+v: got %v want %v", c.iface, got, c.want)
		}
	}
}

func TestInterfacesEnumeratesWithoutShellingOut(t *testing.T) {
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

func TestOnlyLinuxClaimsToBeVerified(t *testing.T) {
	if !(linuxBackend{}).Verified() {
		t.Error("linux is the platform this was written on")
	}
	for _, b := range []IHostBackend{macBackend{}, windowsBackend{}, fallbackBackend{}} {
		if b.Verified() {
			t.Errorf("%s has never been run on its target platform", b.Name())
		}
	}
}

func TestUnverifiedBackendsSaySoWithoutClaimingToBeBroken(t *testing.T) {
	ok, why := macBackend{}.Health()
	if !ok {
		t.Skip("lsof and netstat both absent here")
	}
	if !strings.Contains(why, "never been run") {
		t.Errorf("health note = %q", why)
	}
}

func TestUnknownPlatformExplainsWhatStillWorks(t *testing.T) {
	ok, why := fallbackBackend{}.Health()
	if ok {
		t.Error("an unknown platform cannot list ports")
	}

	if !strings.Contains(why, "manually") {
		t.Errorf("message = %q", why)
	}
}

func TestLinuxFindsAPortWeJustOpened(t *testing.T) {
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
	t.Errorf("did not find the port we just opened (%d) among %d listeners",
		want, len(ports))
}
