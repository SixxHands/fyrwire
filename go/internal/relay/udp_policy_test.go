package relay

import (
	"net"
	"testing"
	"time"
)

func policyListener(t *testing.T, opt UdpOptionsData) (*udpListener, *fakeTime) {
	t.Helper()
	clock := newClock()
	opt.Now = clock.Now
	be, _ := udpEcho(t, "p")
	l := newUDPListener("k", BindingData{"127.0.0.1", freePort(t), "127.0.0.1", be, "udp"},
		nil, opt)
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Stop)
	return l, clock
}

func send(t *testing.T, l *udpListener, from string, payload string) {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp4", from)
	if err != nil {
		t.Fatal(err)
	}
	l.onClient([]byte(payload), addr)
}

func TestUnrepliedSessionExpiresOnTheShortTimer(t *testing.T) {
	l, clock := policyListener(t, UdpOptionsData{})
	send(t, l, "1.2.3.4:5000", "x")
	clock.advance(31 * time.Second)
	if n := l.ReapOnce(); n != 1 {
		t.Errorf("reaped %d, want 1", n)
	}
}

func TestRepliedSessionSurvivesTheShortTimer(t *testing.T) {
	l, clock := policyListener(t, UdpOptionsData{})
	send(t, l, "1.2.3.4:5000", "x")
	l.mu.Lock()
	for _, s := range l.sessions {
		s.replied = true
	}
	l.mu.Unlock()
	clock.advance(31 * time.Second)
	if n := l.ReapOnce(); n != 0 {
		t.Errorf("a replied session must use the long timer, reaped %d", n)
	}
	clock.advance(181 * time.Second)
	if n := l.ReapOnce(); n != 1 {
		t.Errorf("reaped %d, want 1", n)
	}
}

func TestBackendOnlyTrafficKeepsASessionAlive(t *testing.T) {
	l, clock := policyListener(t, UdpOptionsData{})
	send(t, l, "1.2.3.4:5000", "x")
	l.mu.Lock()
	var sess *udpSession
	for _, s := range l.sessions {
		s.replied = true
		sess = s
	}
	l.mu.Unlock()
	for i := 0; i < 10; i++ {
		clock.advance(60 * time.Second)
		l.mu.Lock()
		sess.lastTX = clock.Now()
		l.mu.Unlock()
		if n := l.ReapOnce(); n != 0 {
			t.Fatalf("evicted a session that is still receiving (round %d)", i)
		}
	}
}

func TestSessionTableCapDropsRatherThanQueues(t *testing.T) {
	l, _ := policyListener(t, UdpOptionsData{MaxSessions: 2, MaxPerIP: 100})
	for i := 0; i < 4; i++ {
		send(t, l, net.JoinHostPort("1.2.3.4", itoa(5000+i)), "x")
	}
	l.mu.Lock()
	live := len(l.sessions)
	l.mu.Unlock()
	if live != 2 {
		t.Errorf("table holds %d, cap is 2", live)
	}
	if got := l.dropped.Load(); got != 2 {
		t.Errorf("dropped %d, want 2", got)
	}
}

func TestPerIPCapStopsOneSourceTakingTheTable(t *testing.T) {
	l, _ := policyListener(t, UdpOptionsData{MaxPerIP: 2})
	for i := 0; i < 4; i++ {
		send(t, l, net.JoinHostPort("9.9.9.9", itoa(6000+i)), "x")
	}
	l.mu.Lock()
	live := len(l.sessions)
	l.mu.Unlock()
	if live != 2 {
		t.Errorf("one IP holds %d sessions, cap is 2", live)
	}
}

func TestRateLimiterIsWiredIntoTheDatagramPath(t *testing.T) {
	l, clock := policyListener(t, UdpOptionsData{NewPerSec: 2, MaxPerIP: 100})
	for i := 0; i < 5; i++ {
		send(t, l, net.JoinHostPort("1.2.3.4", itoa(7000+i)), "x")
	}
	l.mu.Lock()
	live := len(l.sessions)
	l.mu.Unlock()
	if live != 2 {
		t.Errorf("the bucket holds 2, opened %d", live)
	}
	clock.advance(time.Second)
	send(t, l, "1.2.3.4:7100", "x")
	l.mu.Lock()
	live = len(l.sessions)
	l.mu.Unlock()
	if live != 3 {
		t.Errorf("the bucket must refill over time, live = %d", live)
	}
}

func TestEvictionKeepsThePerIPIndexCorrect(t *testing.T) {
	l, _ := policyListener(t, UdpOptionsData{})
	send(t, l, "5.5.5.5:1234", "x")
	l.mu.Lock()
	var s *udpSession
	for _, v := range l.sessions {
		s = v
	}
	l.mu.Unlock()
	l.evict(s, "test")
	l.evict(s, "test again")
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := l.perIP["5.5.5.5"]; n != 0 {
		t.Errorf("per-IP count drifted to %d", n)
	}
	if len(l.sessions) != 0 {
		t.Errorf("%d sessions left", len(l.sessions))
	}
}

func TestByteCountersAreCumulative(t *testing.T) {
	l, _ := policyListener(t, UdpOptionsData{})
	send(t, l, "1.2.3.4:5000", "hello")
	before := l.Stats()["c2s_bytes"].(int64)
	if before != 5 {
		t.Fatalf("counted %d bytes", before)
	}
	l.mu.Lock()
	var s *udpSession
	for _, v := range l.sessions {
		s = v
	}
	l.mu.Unlock()
	l.evict(s, "test")
	if after := l.Stats()["c2s_bytes"].(int64); after != before {
		t.Errorf("throughput went backwards: %d -> %d", before, after)
	}
}
