package relay

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

type BindingData struct {
	BindHost   string
	BindPort   int
	TargetHost string
	TargetPort int
	Protocol   string
}

func (s BindingData) bindAddr() string {
	return net.JoinHostPort(s.BindHost, itoa(s.BindPort))
}

func (s BindingData) targetAddr() string {
	return net.JoinHostPort(s.TargetHost, itoa(s.TargetPort))
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

type IRelayListener interface {
	Start() error
	Stop()
	Stats() map[string]any
}

type Manager struct {
	mu        sync.Mutex
	listeners map[string]IRelayListener
	specs     map[string]BindingData
	log       func(string)
	udpOpts   UdpOptionsData
}

func NewManager(log func(string), udp UdpOptionsData) *Manager {
	if log == nil {
		log = func(string) {}
	}
	return &Manager{listeners: map[string]IRelayListener{}, specs: map[string]BindingData{},
		log: log, udpOpts: udp}
}

func (m *Manager) Sync(wanted map[string]BindingData) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var changes []string
	wasBound := map[string]bool{}

	for key, l := range m.listeners {
		if _, keep := wanted[key]; !keep {
			l.Stop()
			delete(m.listeners, key)
			delete(m.specs, key)
			changes = append(changes, "relay "+key+" stopped")
		}
	}

	for key, spec := range wanted {
		if spec.Protocol == "" {
			spec.Protocol = "tcp"
		}
		if cur, ok := m.listeners[key]; ok {
			if m.specs[key] == spec {
				continue
			}
			cur.Stop()

			delete(m.listeners, key)
			delete(m.specs, key)
			wasBound[key] = true
		}
		var l IRelayListener
		if spec.Protocol == "udp" {
			l = newUDPListener(key, spec, m.log, m.udpOpts)
		} else {
			l = newTCPListener(key, spec, m.log)
		}
		if err := l.Start(); err != nil {

			changes = append(changes, fmt.Sprintf(
				"relay %s FAILED to bind %s/%s — %v", key, spec.bindAddr(),
				spec.Protocol, err))
			continue
		}
		m.listeners[key] = l
		m.specs[key] = spec
		verb := "listening"
		if wasBound[key] {
			verb = "rebound"
		}
		changes = append(changes, fmt.Sprintf("relay %s %s %s → %s (%s)",
			key, verb, spec.bindAddr(), spec.targetAddr(), spec.Protocol))
	}
	return changes
}

func (m *Manager) Stats() map[string]map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]map[string]any{}
	for k, l := range m.listeners {
		out[k] = l.Stats()
	}
	return out
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, l := range m.listeners {
		l.Stop()
		delete(m.listeners, k)
	}
}

type tcpListener struct {
	key  string
	spec BindingData
	log  func(string)

	ln       net.Listener
	done     chan struct{}
	wg       sync.WaitGroup
	sem      chan struct{}
	mu       sync.Mutex
	active   int
	total    int
	refused  int
	connTime time.Duration

	conns   map[net.Conn]struct{}
	closing bool
}

const (
	tcpMaxConns       = 64
	tcpConnectTimeout = 5 * time.Second
	tcpBufSize        = 64 * 1024
)

func newTCPListener(key string, spec BindingData, log func(string)) *tcpListener {
	return &tcpListener{key: key, spec: spec, log: log,
		done: make(chan struct{}), sem: make(chan struct{}, tcpMaxConns),
		connTime: tcpConnectTimeout, conns: map[net.Conn]struct{}{}}
}

func (t *tcpListener) Start() error {
	ln, err := net.Listen("tcp", t.spec.bindAddr())
	if err != nil {
		return err
	}
	t.ln = ln
	t.wg.Add(1)
	go t.accept()
	return nil
}

func (t *tcpListener) accept() {
	defer t.wg.Done()
	for {
		conn, err := t.ln.Accept()
		if err != nil {
			select {
			case <-t.done:
				return
			default:
			}

			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			t.log(fmt.Sprintf("relay %s: accept error, retrying — %v", t.key, err))
			time.Sleep(100 * time.Millisecond)
			continue
		}
		select {
		case t.sem <- struct{}{}:
		default:

			t.mu.Lock()
			t.refused++
			t.mu.Unlock()
			conn.Close()
			continue
		}
		t.mu.Lock()
		t.active++
		t.total++
		t.mu.Unlock()
		t.wg.Add(1)
		go t.handle(conn)
	}
}

func (t *tcpListener) handle(client net.Conn) {
	defer t.wg.Done()
	defer func() {
		<-t.sem
		t.mu.Lock()
		t.active--
		delete(t.conns, client)
		t.mu.Unlock()
		client.Close()
	}()

	backend, err := net.DialTimeout("tcp", t.spec.targetAddr(), t.connTime)
	if err != nil {

		return
	}
	defer backend.Close()
	t.mu.Lock()
	if t.closing {

		t.mu.Unlock()
		return
	}
	t.conns[client] = struct{}{}
	t.conns[backend] = struct{}{}
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.conns, backend)
		t.mu.Unlock()
	}()

	setNoDelay(client)
	setNoDelay(backend)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pump(backend, client) }()
	go func() { defer wg.Done(); pump(client, backend) }()
	wg.Wait()
}

func setNoDelay(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
}

func pump(dst, src net.Conn) {
	buf := make([]byte, tcpBufSize)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}

	if tc, ok := dst.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}

// Close the listener and kick every open socket so port rotation never deadlocks waiting on an idle player.
func (t *tcpListener) Stop() {
	close(t.done)
	if t.ln != nil {
		t.ln.Close()
	}

	t.mu.Lock()
	t.closing = true
	for c := range t.conns {
		c.Close()
	}
	t.conns = map[net.Conn]struct{}{}
	t.mu.Unlock()
	t.wg.Wait()
}

func (t *tcpListener) Stats() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return map[string]any{
		"proto": "tcp", "bind": t.spec.bindAddr(), "target": t.spec.targetAddr(),
		"active": t.active, "total": t.total, "refused": t.refused,
	}
}
