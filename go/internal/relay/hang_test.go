package relay

import (
	"net"
	"sync"
	"testing"
	"time"
)

func quietBackend(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		mu.Lock()
		for _, c := range held {
			c.Close()
		}
		mu.Unlock()
	})
	return ln.Addr().(*net.TCPAddr).Port
}

func TestStopReturnsWithAnIdleConnectionOpen(t *testing.T) {
	be := quietBackend(t)
	front := freePort(t)
	l := newTCPListener("a", BindingData{"127.0.0.1", front, "127.0.0.1", be, "tcp"},
		func(string) {})
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(front)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	time.Sleep(150 * time.Millisecond)

	done := make(chan struct{})
	go func() { l.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() hung with one live idle connection — this deadlocks " +
			"every later Sync(), Stats() and Shutdown()")
	}
}

func TestAPortRotationCompletesWhileAConnectionIsLive(t *testing.T) {
	be := quietBackend(t)
	m := NewManager(func(string) {}, UdpOptionsData{})
	defer m.Shutdown()
	first := freePort(t)
	m.Sync(map[string]BindingData{"a": {"127.0.0.1", first, "127.0.0.1", be, "tcp"}})

	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(first)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(150 * time.Millisecond)

	second := freePort(t)
	done := make(chan struct{})
	go func() {
		m.Sync(map[string]BindingData{"a": {"127.0.0.1", second, "127.0.0.1", be, "tcp"}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a port rotation deadlocked the relay manager")
	}
	if _, ok := m.Stats()["a"]; !ok {
		t.Error("the rotated relay should be live on the new port")
	}
}

func TestUdpStopReturnsWhileDatagramsAreArriving(t *testing.T) {
	be, _ := udpEcho(t, "x")
	front := freePort(t)
	l := newUDPListener("a", BindingData{"127.0.0.1", front, "127.0.0.1", be, "udp"},
		func(string) {}, UdpOptionsData{})
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c, err := net.DialUDP("udp4", nil,
				&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: front})
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
		}(i)
	}
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() { l.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		close(stop)
		wg.Wait()
		t.Fatal("udp Stop() hung with datagrams in flight")
	}
	close(stop)
	wg.Wait()
}

func TestUdpRotationUnderTrafficCompletes(t *testing.T) {
	be, _ := udpEcho(t, "x")
	m := NewManager(func(string) {}, UdpOptionsData{})
	defer m.Shutdown()
	first := freePort(t)
	m.Sync(map[string]BindingData{"a": {"127.0.0.1", first, "127.0.0.1", be, "udp"}})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, _ := net.DialUDP("udp4", nil,
			&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: first})
		if c == nil {
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
	}()
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		m.Sync(map[string]BindingData{"a": {"127.0.0.1", freePort(t), "127.0.0.1", be, "udp"}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		close(stop)
		wg.Wait()
		t.Fatal("a udp rotation deadlocked under live traffic")
	}
	close(stop)
	wg.Wait()
}

func TestNoSessionCanBeOpenedAfterStopHasWalkedTheTable(t *testing.T) {
	be, _ := udpEcho(t, "x")
	front := freePort(t)
	l := newUDPListener("a", BindingData{"127.0.0.1", front, "127.0.0.1", be, "udp"},
		func(string) {}, UdpOptionsData{})
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	l.Stop()

	addr, _ := net.ResolveUDPAddr("udp4", "1.2.3.4:5000")
	l.onClient([]byte("late"), addr)

	l.mu.Lock()
	live := len(l.sessions)
	l.mu.Unlock()
	if live != 0 {
		t.Fatalf("a datagram arriving after Stop() opened %d session(s) — "+
			"each one leaks a socket and hangs Stop() forever", live)
	}
	if l.total.Load() != 0 {
		t.Errorf("total = %d, want 0", l.total.Load())
	}
}
