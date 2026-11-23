package listeners_test

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sixxhands/fyrwire/internal/network/relay"
)

func udpEcho(t *testing.T, tag string) (int, func() []string) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	var mu sync.Mutex
	var seen []string
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			mu.Lock()
			seen = append(seen, addr.String())
			mu.Unlock()
			conn.WriteToUDP(append([]byte(tag+":"), buf[:n]...), addr)
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).Port, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func tcpEcho(t *testing.T, tag string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 256)
				n, _ := c.Read(buf)
				c.Write(append([]byte(tag+":"), buf[:n]...))
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

func udpRoundtrip(t *testing.T, port int, message string) string {
	t.Helper()
	c, err := net.DialUDP("udp4", nil,
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 65535)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("no reply: %v", err)
	}
	return string(buf[:n])
}

func createTestManager() *relay.Manager {
	return relay.ManagerFactory{}.Create(relay.CreateManagerBlueprint(nil, relay.UdpOptionsData{}))
}

func Test_udp_datagram_round_trips(t *testing.T) {
	backend, _ := udpEcho(t, "one")
	m := createTestManager()
	defer m.Stop()
	front := freePort(t)
	m.Sync(map[string]relay.BindingData{"a": {BindHost: "127.0.0.1", BindPort: front, TargetHost: "127.0.0.1", TargetPort: backend, Protocol: "udp"}})
	if got := udpRoundtrip(t, front, "hello"); got != "one:hello" {
		t.Errorf("got %q", got)
	}
}

func Test_two_clients_get_separate_upstream_sockets(t *testing.T) {
	backend, seen := udpEcho(t, "x")
	m := createTestManager()
	defer m.Stop()
	front := freePort(t)
	m.Sync(map[string]relay.BindingData{"a": {BindHost: "127.0.0.1", BindPort: front, TargetHost: "127.0.0.1", TargetPort: backend, Protocol: "udp"}})
	udpRoundtrip(t, front, "aaa")
	udpRoundtrip(t, front, "bbb")
	ports := map[string]bool{}
	for _, a := range seen() {
		ports[a] = true
	}
	if len(ports) != 2 {
		t.Errorf("expected 2 upstream ports, got %v", ports)
	}
}

func Test_reply_source_port_is_granted_port(t *testing.T) {
	backend, _ := udpEcho(t, "src")
	m := createTestManager()
	defer m.Stop()
	front := freePort(t)
	m.Sync(map[string]relay.BindingData{"a": {BindHost: "127.0.0.1", BindPort: front, TargetHost: "127.0.0.1", TargetPort: backend, Protocol: "udp"}})

	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	c.WriteToUDP([]byte("q"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: front})
	buf := make([]byte, 4096)
	n, from, err := c.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "src:q" {
		t.Errorf("got %q", buf[:n])
	}
	if from.Port != front {
		t.Errorf("reply came from :%d, want :%d", from.Port, front)
	}
}

func Test_unchanged_listener_is_not_rebound(t *testing.T) {
	backend, _ := udpEcho(t, "z")
	m := createTestManager()
	defer m.Stop()
	front := freePort(t)
	binding := map[string]relay.BindingData{"a": {BindHost: "127.0.0.1", BindPort: front, TargetHost: "127.0.0.1", TargetPort: backend, Protocol: "udp"}}
	m.Sync(binding)
	if changes := m.Sync(binding); len(changes) != 0 {
		t.Errorf("unexpected rebind: %v", changes)
	}
}

func Test_protocol_is_part_of_binding(t *testing.T) {
	a := relay.BindingData{BindHost: "127.0.0.1", BindPort: 1, TargetHost: "127.0.0.1", TargetPort: 2, Protocol: "tcp"}
	b := relay.BindingData{BindHost: "127.0.0.1", BindPort: 1, TargetHost: "127.0.0.1", TargetPort: 2, Protocol: "udp"}
	if a == b {
		t.Fatal("tcp and udp bindings must not compare equal")
	}
}

func Test_rotating_one_port_leaves_other_alone(t *testing.T) {
	be1, _ := udpEcho(t, "p")
	be2, _ := udpEcho(t, "q")
	m := createTestManager()
	defer m.Stop()
	f1, f2 := freePort(t), freePort(t)
	m.Sync(map[string]relay.BindingData{
		"a": {BindHost: "127.0.0.1", BindPort: f1, TargetHost: "127.0.0.1", TargetPort: be1, Protocol: "udp"},
		"b": {BindHost: "127.0.0.1", BindPort: f2, TargetHost: "127.0.0.1", TargetPort: be2, Protocol: "udp"}})
	f1b := freePort(t)
	changes := m.Sync(map[string]relay.BindingData{
		"a": {BindHost: "127.0.0.1", BindPort: f1b, TargetHost: "127.0.0.1", TargetPort: be1, Protocol: "udp"},
		"b": {BindHost: "127.0.0.1", BindPort: f2, TargetHost: "127.0.0.1", TargetPort: be2, Protocol: "udp"}})
	if len(changes) != 1 {
		t.Errorf("only 'a' should have changed: %v", changes)
	}
	if got := udpRoundtrip(t, f2, "still"); got != "q:still" {
		t.Errorf("b was disturbed: %q", got)
	}
}

func Test_bind_failure_on_one_key_spares_others(t *testing.T) {
	backend, _ := udpEcho(t, "ok")
	blocker, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	taken := blocker.LocalAddr().(*net.UDPAddr).Port
	m := createTestManager()
	defer m.Stop()
	good := freePort(t)
	changes := m.Sync(map[string]relay.BindingData{
		"bad":  {BindHost: "127.0.0.1", BindPort: taken, TargetHost: "127.0.0.1", TargetPort: backend, Protocol: "udp"},
		"good": {BindHost: "127.0.0.1", BindPort: good, TargetHost: "127.0.0.1", TargetPort: backend, Protocol: "udp"}})
	sawFailure := false
	for _, c := range changes {
		if strings.Contains(c, "FAILED") {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Errorf("expected a bind failure: %v", changes)
	}
	if _, ok := m.Stats()["bad"]; ok {
		t.Error("failed listener must not be listed as live")
	}
	if got := udpRoundtrip(t, good, "fine"); got != "ok:fine" {
		t.Errorf("other relay was disturbed: %q", got)
	}
}

func Test_tcp_relay_forwards(t *testing.T) {
	backend := tcpEcho(t, "t")
	m := createTestManager()
	defer m.Stop()
	front := freePort(t)
	m.Sync(map[string]relay.BindingData{"a": {BindHost: "127.0.0.1", BindPort: front, TargetHost: "127.0.0.1", TargetPort: backend, Protocol: "tcp"}})
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(front)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	c.Write([]byte("hi"))
	buf := make([]byte, 64)
	n, _ := c.Read(buf)
	if string(buf[:n]) != "t:hi" {
		t.Errorf("got %q", buf[:n])
	}
}

func Test_stats_shape_for_both_protocols(t *testing.T) {
	be, _ := udpEcho(t, "s")
	tbe := tcpEcho(t, "s")
	m := createTestManager()
	defer m.Stop()
	m.Sync(map[string]relay.BindingData{
		"u": {BindHost: "127.0.0.1", BindPort: freePort(t), TargetHost: "127.0.0.1", TargetPort: be, Protocol: "udp"},
		"t": {BindHost: "127.0.0.1", BindPort: freePort(t), TargetHost: "127.0.0.1", TargetPort: tbe, Protocol: "tcp"}})
	st := m.Stats()
	if st["u"]["proto"] != "udp" || st["t"]["proto"] != "tcp" {
		t.Errorf("stats: %v", st)
	}
	if _, ok := st["u"]["sessions"]; !ok {
		t.Error("udp stats missing sessions")
	}
	if _, ok := st["t"]["active"]; !ok {
		t.Error("tcp stats missing active")
	}
}
