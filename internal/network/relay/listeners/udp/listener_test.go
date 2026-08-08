package udp

import (
	"net"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
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

func hostPort(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

func buildTestListener(t *testing.T, key, bindAddr, targetAddr string, log func(string), options OptionsData) *Listener {
	t.Helper()
	bp, err := CreateListenerBlueprint(key, bindAddr, targetAddr, log, options)
	if err != nil {
		t.Fatal(err)
	}
	return ListenerFactory{}.Create(bp)
}

func startTestListener(t *testing.T, front, backend int, options OptionsData) *Listener {
	t.Helper()
	l := buildTestListener(t, "a", hostPort(front), hostPort(backend), func(string) {}, options)
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestConcurrentDatagramsFromOneClientOpenOneSession(t *testing.T) {
	backend, _ := udpEcho(t, "r")
	l := startTestListener(t, freePort(t), backend, OptionsData{})
	defer l.Stop()

	addr, _ := net.ResolveUDPAddr("udp4", "1.2.3.4:5000")
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			l.onClient([]byte("x"), addr)
		}()
	}
	close(start)
	wg.Wait()

	if n := l.total.Load(); n != 1 {
		t.Errorf("one client opened %d sessions, want 1", n)
	}
}

func TestPortCanBeReboundImmediatelyAfterStop(t *testing.T) {
	backend, _ := udpEcho(t, "z")
	front := freePort(t)
	first := startTestListener(t, front, backend, OptionsData{})
	first.Stop()
	second := startTestListener(t, front, backend, OptionsData{})
	second.Stop()
}

func TestStopWaitsForGoroutines(t *testing.T) {
	dead, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer dead.Close()
	l := startTestListener(t, freePort(t), dead.LocalAddr().(*net.UDPAddr).Port, OptionsData{})
	for i := 0; i < 24; i++ {
		addr, _ := net.ResolveUDPAddr("udp4", net.JoinHostPort("1.2.3.4", strconv.Itoa(6000+i)))
		l.onClient([]byte("x"), addr)
	}
	base := runtime.NumGoroutine()
	if base < 24 {
		t.Skipf("expected session goroutines running, saw %d", base)
	}
	l.Stop()
	if after := runtime.NumGoroutine(); after > base-20 {
		t.Errorf("Stop() returned with %d goroutines still running (was %d)", after, base)
	}
}

func TestZeroLengthDatagramCreatesNoSession(t *testing.T) {
	backend, seen := udpEcho(t, "z")
	front := freePort(t)
	l := startTestListener(t, front, backend, OptionsData{DropZeroLength: true})
	defer l.Stop()
	c, _ := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: front})
	c.Write(nil)
	c.Close()
	time.Sleep(150 * time.Millisecond)
	if n := l.total.Load(); n != 0 {
		t.Errorf("opened %d sessions for empty datagram", n)
	}
	if got := seen(); len(got) != 0 {
		t.Errorf("empty datagram reached backend: %v", got)
	}
}

func TestMaxDatagramRoundTripsWithoutTruncation(t *testing.T) {
	backend, _ := udpEcho(t, "big")
	front := freePort(t)
	l := startTestListener(t, front, backend, OptionsData{})
	defer l.Stop()

	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: front})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	data := make([]byte, 60000)
	for i := range data {
		data[i] = 'y'
	}
	if _, err := c.Write(data); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 65535)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(data)+4 {
		t.Errorf("truncated: got %d bytes, want %d", n, len(data)+4)
	}
}

func TestNoSessionOpenedAfterStop(t *testing.T) {
	backend, _ := udpEcho(t, "x")
	l := startTestListener(t, freePort(t), backend, OptionsData{})
	l.Stop()

	addr, _ := net.ResolveUDPAddr("udp4", "1.2.3.4:5000")
	l.onClient([]byte("late"), addr)
	if live := liveSessions(l); live != 0 || l.total.Load() != 0 {
		t.Fatalf("opened session after Stop(): live=%d total=%d", live, l.total.Load())
	}
}

func TestUdpStopReturnsWhileDatagramsArrive(t *testing.T) {
	backend, _ := udpEcho(t, "x")
	front := freePort(t)
	l := startTestListener(t, front, backend, OptionsData{})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go sendUdpFlood(&wg, stop, front)
	}
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() { l.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		close(stop)
		wg.Wait()
		t.Fatal("udp Stop() hung with active traffic")
	}
	close(stop)
	wg.Wait()
}

func sendUdpFlood(wg *sync.WaitGroup, stop <-chan struct{}, front int) {
	defer wg.Done()
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: front})
	if err != nil {
		return
	}
	defer c.Close()
	for {
		select {
		case <-stop:
			return
		default:
		}
		c.Write([]byte("x"))
		time.Sleep(time.Millisecond)
	}
}

type manualTime struct {
	mu sync.Mutex
	t  time.Time
}

func createManualTime() *manualTime {
	return &manualTime{t: time.Unix(1_000_000, 0)}
}

func (m *manualTime) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.t
}

func (m *manualTime) advance(d time.Duration) {
	m.mu.Lock()
	m.t = m.t.Add(d)
	m.mu.Unlock()
}

func policyListener(t *testing.T, options OptionsData) (*Listener, *manualTime) {
	t.Helper()
	tm := createManualTime()
	options.Now = tm.Now
	backend, _ := udpEcho(t, "p")
	l := buildTestListener(t, "k", hostPort(freePort(t)), hostPort(backend), nil, options)
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Stop)
	return l, tm
}

func sendDatagram(t *testing.T, l *Listener, from string, message string) {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp4", from)
	if err != nil {
		t.Fatal(err)
	}
	l.onClient([]byte(message), addr)
}

func TestUnrepliedSessionExpiresOnShortTimer(t *testing.T) {
	l, tm := policyListener(t, OptionsData{})
	sendDatagram(t, l, "1.2.3.4:5000", "x")
	tm.advance(31 * time.Second)
	if n := l.ReapOnce(); n != 1 {
		t.Errorf("reaped %d, want 1", n)
	}
}

func TestRepliedSessionUsesLongTimer(t *testing.T) {
	l, tm := policyListener(t, OptionsData{})
	sendDatagram(t, l, "1.2.3.4:5000", "x")
	l.mu.Lock()
	for _, s := range l.sessions {
		s.replied = true
	}
	l.mu.Unlock()
	tm.advance(31 * time.Second)
	if n := l.ReapOnce(); n != 0 {
		t.Errorf("replied session reaped too early: %d", n)
	}
	tm.advance(181 * time.Second)
	if n := l.ReapOnce(); n != 1 {
		t.Errorf("reaped %d, want 1", n)
	}
}

func TestBackendOnlyTrafficKeepsSessionAlive(t *testing.T) {
	l, tm := policyListener(t, OptionsData{})
	sendDatagram(t, l, "1.2.3.4:5000", "x")
	l.mu.Lock()
	var active *udpSession
	for _, s := range l.sessions {
		s.replied = true
		active = s
	}
	l.mu.Unlock()
	for i := 0; i < 10; i++ {
		tm.advance(60 * time.Second)
		l.mu.Lock()
		active.lastTx = tm.Now()
		l.mu.Unlock()
		if n := l.ReapOnce(); n != 0 {
			t.Fatalf("evicted active receiving session (round %d)", i)
		}
	}
}

func liveSessions(l *Listener) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.sessions)
}

func firstSession(l *Listener) *udpSession {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.sessions {
		return s
	}
	return nil
}

func TestSessionTableCapDropsOverflow(t *testing.T) {
	l, _ := policyListener(t, OptionsData{MaxSessions: 2, MaxPerIp: 100})
	for i := 0; i < 4; i++ {
		sendDatagram(t, l, net.JoinHostPort("1.2.3.4", strconv.Itoa(5000+i)), "x")
	}
	if live := liveSessions(l); live != 2 || l.dropped.Load() != 2 {
		t.Errorf("live=%d dropped=%d, want 2 and 2", live, l.dropped.Load())
	}
}

func TestPerIpCapLimitsSingleSource(t *testing.T) {
	l, _ := policyListener(t, OptionsData{MaxPerIp: 2})
	for i := 0; i < 4; i++ {
		sendDatagram(t, l, net.JoinHostPort("9.9.9.9", strconv.Itoa(6000+i)), "x")
	}
	if live := liveSessions(l); live != 2 {
		t.Errorf("live=%d, want 2", live)
	}
}

func TestRateLimiterLimitsNewSessions(t *testing.T) {
	l, tm := policyListener(t, OptionsData{NewPerSec: 2, MaxPerIp: 100})
	for i := 0; i < 5; i++ {
		sendDatagram(t, l, net.JoinHostPort("1.2.3.4", strconv.Itoa(7000+i)), "x")
	}
	if live := liveSessions(l); live != 2 {
		t.Errorf("opened %d, want 2", live)
	}
	tm.advance(time.Second)
	sendDatagram(t, l, "1.2.3.4:7100", "x")
	if live := liveSessions(l); live != 3 {
		t.Errorf("bucket should refill, live=%d", live)
	}
}

func TestEvictionUpdatesPerIpCount(t *testing.T) {
	l, _ := policyListener(t, OptionsData{})
	sendDatagram(t, l, "5.5.5.5:1234", "x")
	s := firstSession(l)
	l.evict(s)
	l.evict(s)
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := l.perIp["5.5.5.5"]; n != 0 || len(l.sessions) != 0 {
		t.Errorf("perIp=%d sessions=%d", n, len(l.sessions))
	}
}

func TestByteCountersAreCumulative(t *testing.T) {
	l, _ := policyListener(t, OptionsData{})
	sendDatagram(t, l, "1.2.3.4:5000", "hello")
	before := l.Stats()["c2s_bytes"].(int64)
	if before != 5 {
		t.Fatalf("counted %d bytes", before)
	}
	l.evict(firstSession(l))
	if after := l.Stats()["c2s_bytes"].(int64); after != before {
		t.Errorf("bytes went backwards: %d -> %d", before, after)
	}
}
