package listeners_test

import (
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sixxhands/fyrwire/internal/network/relay"
	"github.com/sixxhands/fyrwire/internal/network/relay/listeners/tcp"
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

func Test_stop_returns_with_idle_connection_open(t *testing.T) {
	backend := quietBackend(t)
	front := freePort(t)
	bp, err := tcp.CreateListenerBlueprint("a", net.JoinHostPort("127.0.0.1", strconv.Itoa(front)), net.JoinHostPort("127.0.0.1", strconv.Itoa(backend)), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	l := tcp.ListenerFactory{}.Create(bp)
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(front)))
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
		t.Fatal("Stop() hung with an open idle connection")
	}
}

func Test_port_rotation_completes_while_connection_is_live(t *testing.T) {
	backend := quietBackend(t)
	m := relay.ManagerFactory{}.Create(relay.CreateManagerBlueprint(func(string) {}, relay.UdpOptionsData{}))
	defer m.Stop()
	first := freePort(t)
	m.Sync(map[string]relay.BindingData{"a": {BindHost: "127.0.0.1", BindPort: first, TargetHost: "127.0.0.1", TargetPort: backend, Protocol: "tcp"}})

	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(first)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(150 * time.Millisecond)

	second := freePort(t)
	done := make(chan struct{})
	go func() {
		m.Sync(map[string]relay.BindingData{"a": {BindHost: "127.0.0.1", BindPort: second, TargetHost: "127.0.0.1", TargetPort: backend, Protocol: "tcp"}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("port rotation deadlocked relay manager")
	}
	if _, ok := m.Stats()["a"]; !ok {
		t.Error("rotated relay should be live on new port")
	}
}

func Test_udp_rotation_under_traffic_completes(t *testing.T) {
	backend, _ := udpEcho(t, "x")
	m := relay.ManagerFactory{}.Create(relay.CreateManagerBlueprint(func(string) {}, relay.UdpOptionsData{}))
	defer m.Stop()
	first := freePort(t)
	m.Sync(map[string]relay.BindingData{"a": {BindHost: "127.0.0.1", BindPort: first, TargetHost: "127.0.0.1", TargetPort: backend, Protocol: "udp"}})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, _ := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: first})
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
		m.Sync(map[string]relay.BindingData{"a": {BindHost: "127.0.0.1", BindPort: freePort(t), TargetHost: "127.0.0.1", TargetPort: backend, Protocol: "udp"}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		close(stop)
		wg.Wait()
		t.Fatal("udp rotation deadlocked under live traffic")
	}
	close(stop)
	wg.Wait()
}
