package relay

import (
	"net"
	"sync"
	"testing"
	"time"
)

type fakeTime struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeTime {
	return &fakeTime{t: time.Unix(1_000_000, 0)}
}

func (c *fakeTime) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeTime) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

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

func udpRoundtrip(t *testing.T, port int, payload string) string {
	t.Helper()
	c, err := net.DialUDP("udp4", nil,
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 65535)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("no reply: %v", err)
	}
	return string(buf[:n])
}

func TestUDPDatagramRoundTrips(t *testing.T) {
	be, _ := udpEcho(t, "one")
	m := NewManager(nil, UdpOptionsData{})
	defer m.Shutdown()
	front := freePort(t)
	m.Sync(map[string]BindingData{"a": {"127.0.0.1", front, "127.0.0.1", be, "udp"}})
	if got := udpRoundtrip(t, front, "hello"); got != "one:hello" {
		t.Errorf("got %q", got)
	}
}

func TestTwoClientsGetSeparateUpstreamSockets(t *testing.T) {
	be, seen := udpEcho(t, "x")
	m := NewManager(nil, UdpOptionsData{})
	defer m.Shutdown()
	front := freePort(t)
	m.Sync(map[string]BindingData{"a": {"127.0.0.1", front, "127.0.0.1", be, "udp"}})
	udpRoundtrip(t, front, "aaa")
	udpRoundtrip(t, front, "bbb")
	ports := map[string]bool{}
	for _, a := range seen() {
		ports[a] = true
	}
	if len(ports) != 2 {
		t.Errorf("each client must reach the backend from its own port, got %v", ports)
	}
}

func TestReplySourcePortIsTheGrantedPort(t *testing.T) {
	be, _ := udpEcho(t, "src")
	m := NewManager(nil, UdpOptionsData{})
	defer m.Shutdown()
	front := freePort(t)
	m.Sync(map[string]BindingData{"a": {"127.0.0.1", front, "127.0.0.1", be, "udp"}})

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
		t.Errorf("payload %q", buf[:n])
	}
	if from.Port != front {
		t.Errorf("reply came from :%d, must be the granted :%d — any other "+
			"port is dropped by a client behind a NAT", from.Port, front)
	}
}

func TestBackToBackDatagramsOpenOneSession(t *testing.T) {
	be, seen := udpEcho(t, "r")
	m := NewManager(nil, UdpOptionsData{})
	defer m.Shutdown()
	front := freePort(t)
	m.Sync(map[string]BindingData{"a": {"127.0.0.1", front, "127.0.0.1", be, "udp"}})

	c, _ := net.DialUDP("udp4", nil,
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: front})
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	c.Write([]byte("1"))
	c.Write([]byte("2"))
	buf := make([]byte, 4096)
	c.Read(buf)
	c.Read(buf)
	ports := map[string]bool{}
	for _, a := range seen() {
		ports[a] = true
	}
	if len(ports) != 1 {
		t.Errorf("one client must not open two upstream sockets, got %v", ports)
	}
}

func TestUnchangedListenerIsNotRebound(t *testing.T) {
	be, _ := udpEcho(t, "z")
	m := NewManager(nil, UdpOptionsData{})
	defer m.Shutdown()
	front := freePort(t)
	spec := map[string]BindingData{"a": {"127.0.0.1", front, "127.0.0.1", be, "udp"}}
	m.Sync(spec)
	if changes := m.Sync(spec); len(changes) != 0 {
		t.Errorf("a rebind would destroy every live session: %v", changes)
	}
}

func TestProtocolIsPartOfTheSpec(t *testing.T) {
	a := BindingData{"127.0.0.1", 1, "127.0.0.1", 2, "tcp"}
	b := BindingData{"127.0.0.1", 1, "127.0.0.1", 2, "udp"}
	if a == b {
		t.Fatal("tcp and udp specs must not compare equal")
	}
}

func TestPortCanBeReboundImmediatelyAfterStop(t *testing.T) {
	be, _ := udpEcho(t, "z")
	front := freePort(t)
	spec := BindingData{"127.0.0.1", front, "127.0.0.1", be, "udp"}
	first := newUDPListener("a", spec, nil, UdpOptionsData{})
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	first.Stop()
	second := newUDPListener("a", spec, nil, UdpOptionsData{})
	if err := second.Start(); err != nil {
		t.Fatalf("rebind failed — Stop() returned before the port was free: %v", err)
	}
	second.Stop()
}

func TestRotatingOnePortLeavesTheOtherAlone(t *testing.T) {
	be1, _ := udpEcho(t, "p")
	be2, _ := udpEcho(t, "q")
	m := NewManager(nil, UdpOptionsData{})
	defer m.Shutdown()
	f1, f2 := freePort(t), freePort(t)
	m.Sync(map[string]BindingData{
		"a": {"127.0.0.1", f1, "127.0.0.1", be1, "udp"},
		"b": {"127.0.0.1", f2, "127.0.0.1", be2, "udp"}})
	f1b := freePort(t)
	changes := m.Sync(map[string]BindingData{
		"a": {"127.0.0.1", f1b, "127.0.0.1", be1, "udp"},
		"b": {"127.0.0.1", f2, "127.0.0.1", be2, "udp"}})
	if len(changes) != 1 {
		t.Errorf("only 'a' should have changed: %v", changes)
	}
	if got := udpRoundtrip(t, f2, "still"); got != "q:still" {
		t.Errorf("b was disturbed: %q", got)
	}
}

func TestZeroLengthDatagramCreatesNoSession(t *testing.T) {
	be, seen := udpEcho(t, "z")
	front := freePort(t)
	l := newUDPListener("a", BindingData{"127.0.0.1", front, "127.0.0.1", be, "udp"},
		nil, UdpOptionsData{DropZeroLength: true})
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	defer l.Stop()
	c, _ := net.DialUDP("udp4", nil,
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: front})
	c.Write(nil)
	c.Close()
	time.Sleep(150 * time.Millisecond)
	if n := l.total.Load(); n != 0 {
		t.Errorf("a port scan must cost nothing, opened %d sessions", n)
	}
	if got := seen(); len(got) != 0 {
		t.Errorf("nothing should have reached the backend: %v", got)
	}
}

func TestMaxDatagramRoundTripsWithoutTruncation(t *testing.T) {
	be, _ := udpEcho(t, "big")
	m := NewManager(nil, UdpOptionsData{})
	defer m.Shutdown()
	front := freePort(t)
	m.Sync(map[string]BindingData{"a": {"127.0.0.1", front, "127.0.0.1", be, "udp"}})
	payload := make([]byte, 60000)
	for i := range payload {
		payload[i] = 'y'
	}
	got := udpRoundtrip(t, front, string(payload))
	if len(got) != len(payload)+4 {
		t.Errorf("truncated: got %d bytes, want %d", len(got), len(payload)+4)
	}
}

func TestBindFailureOnOneKeySparesTheOthers(t *testing.T) {
	be, _ := udpEcho(t, "ok")
	blocker, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	taken := blocker.LocalAddr().(*net.UDPAddr).Port
	m := NewManager(nil, UdpOptionsData{})
	defer m.Shutdown()
	good := freePort(t)
	changes := m.Sync(map[string]BindingData{
		"bad":  {"127.0.0.1", taken, "127.0.0.1", be, "udp"},
		"good": {"127.0.0.1", good, "127.0.0.1", be, "udp"}})
	sawFailure := false
	for _, c := range changes {
		if len(c) > 0 && contains(c, "FAILED") {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Errorf("expected a bind failure: %v", changes)
	}
	if _, ok := m.Stats()["bad"]; ok {
		t.Error("a listener that failed to bind is not live")
	}
	if got := udpRoundtrip(t, good, "fine"); got != "ok:fine" {
		t.Errorf("the other relay was disturbed: %q", got)
	}
}

func TestTCPRelayForwards(t *testing.T) {
	be := tcpEcho(t, "t")
	m := NewManager(nil, UdpOptionsData{})
	defer m.Shutdown()
	front := freePort(t)
	m.Sync(map[string]BindingData{"a": {"127.0.0.1", front, "127.0.0.1", be, "tcp"}})
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(front)))
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

func TestStatsShapeForBothProtocols(t *testing.T) {
	be, _ := udpEcho(t, "s")
	tbe := tcpEcho(t, "s")
	m := NewManager(nil, UdpOptionsData{})
	defer m.Shutdown()
	m.Sync(map[string]BindingData{
		"u": {"127.0.0.1", freePort(t), "127.0.0.1", be, "udp"},
		"t": {"127.0.0.1", freePort(t), "127.0.0.1", tbe, "tcp"}})
	st := m.Stats()
	if st["u"]["proto"] != "udp" || st["t"]["proto"] != "tcp" {
		t.Errorf("stats: %v", st)
	}
	if _, ok := st["u"]["sessions"]; !ok {
		t.Error("udp stats must report sessions")
	}
	if _, ok := st["t"]["active"]; !ok {
		t.Error("tcp stats must report active connections")
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
