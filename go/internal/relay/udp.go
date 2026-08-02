package relay

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type UdpOptionsData struct {
	IdleNew         time.Duration
	IdleEstablished time.Duration
	MaxSessions     int
	MaxPerIP        int
	NewPerSec       float64
	ReapInterval    time.Duration
	DropZeroLength  bool

	MaxSessionsSet     bool
	MaxPerIPSet        bool
	NewPerSecSet       bool
	IdleNewSet         bool
	IdleEstablishedSet bool

	Now func() time.Time
}

// Clamp the session cap to what the OS actually lets us open. Running out of file descriptors mid-game is not fun.
func clampSessions(cap int) int {
	if cap <= 0 {
		return cap
	}
	if lo, hi, ok := localPortRange(); ok && hi-lo < cap {
		cap = hi - lo
	}
	if n, ok := maxOpenFiles(); ok {
		if room := n - 128; room > 16 && room < cap {
			cap = room
		}
	}
	if cap < 1 {
		return 1
	}
	return cap
}

func (o UdpOptionsData) withDefaults() UdpOptionsData {
	if o.IdleNew == 0 && !o.IdleNewSet {

		o.IdleNew = 30 * time.Second
	}
	if o.IdleEstablished == 0 && !o.IdleEstablishedSet {

		o.IdleEstablished = 180 * time.Second
	}
	if o.MaxSessions == 0 && !o.MaxSessionsSet {
		o.MaxSessions = 512
	}
	o.MaxSessions = clampSessions(o.MaxSessions)
	if o.MaxPerIP == 0 && !o.MaxPerIPSet {
		o.MaxPerIP = 8
	}
	if o.NewPerSec == 0 && !o.NewPerSecSet {
		o.NewPerSec = 32
	}
	if o.ReapInterval == 0 {
		o.ReapInterval = 5 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

type udpSession struct {
	client   *net.UDPAddr
	key      string
	conn     *net.UDPConn
	lastRX   time.Time
	lastTX   time.Time
	replied  bool
	done     chan struct{}
	closeOne sync.Once
}

type udpListener struct {
	key  string
	spec BindingData
	log  func(string)
	opt  UdpOptionsData

	conn    *net.UDPConn
	backend *net.UDPAddr
	done    chan struct{}
	wg      sync.WaitGroup

	mu       sync.Mutex
	closing  bool
	sessions map[string]*udpSession
	perIP    map[string]int
	tokens   float64
	tokensAt time.Time

	total          atomic.Int64
	dropped        atomic.Int64
	sendDropped    atomic.Int64
	upstreamErrors atomic.Int64
	listenerErrors atomic.Int64
	openFailed     atomic.Int64

	c2sBytes atomic.Int64
	s2cBytes atomic.Int64
}

func newUDPListener(key string, spec BindingData, log func(string), opt UdpOptionsData) *udpListener {
	o := opt.withDefaults()
	return &udpListener{key: key, spec: spec, log: log, opt: o,
		done: make(chan struct{}), sessions: map[string]*udpSession{},
		perIP: map[string]int{}, tokens: o.NewPerSec, tokensAt: o.Now()}
}

func (u *udpListener) Start() error {

	backend, err := net.ResolveUDPAddr("udp4", u.spec.targetAddr())
	if err != nil {
		return err
	}
	addr, err := net.ResolveUDPAddr("udp4", u.spec.bindAddr())
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return err
	}
	u.backend, u.conn = backend, conn
	u.wg.Add(2)
	go u.readClients()
	go u.reaper()
	return nil
}

func (u *udpListener) readClients() {
	defer u.wg.Done()

	buf := make([]byte, 65535)
	for {
		n, addr, err := u.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-u.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}

			u.listenerErrors.Add(1)
			continue
		}
		u.onClient(buf[:n], addr)
	}
}

func (u *udpListener) onClient(data []byte, addr *net.UDPAddr) {
	if len(data) == 0 && u.opt.DropZeroLength {

		return
	}
	key := addr.String()

	u.mu.Lock()
	if u.closing {

		u.mu.Unlock()
		u.dropped.Add(1)
		return
	}
	s, ok := u.sessions[key]
	if !ok {
		if len(u.sessions) >= u.opt.MaxSessions ||
			u.perIP[addr.IP.String()] >= u.opt.MaxPerIP || !u.allowNewLocked() {

			u.mu.Unlock()
			u.dropped.Add(1)
			return
		}
		var err error
		s, err = u.openLocked(addr, key)
		if err != nil {
			u.mu.Unlock()
			u.openFailed.Add(1)
			return
		}
	}
	s.lastRX = u.opt.Now()
	conn := s.conn
	u.mu.Unlock()

	u.c2sBytes.Add(int64(len(data)))

	_ = conn.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := conn.Write(data); err != nil {
		u.sendDropped.Add(1)
	}
}

func (u *udpListener) openLocked(addr *net.UDPAddr, key string) (*udpSession, error) {
	conn, err := net.DialUDP("udp4", nil, u.backend)
	if err != nil {
		return nil, err
	}
	now := u.opt.Now()
	s := &udpSession{client: addr, key: key, conn: conn, lastRX: now, lastTX: now,
		done: make(chan struct{})}
	u.sessions[key] = s
	u.perIP[addr.IP.String()]++
	u.total.Add(1)
	u.wg.Add(1)
	go u.readBackend(s)
	return s, nil
}

func (u *udpListener) readBackend(s *udpSession) {
	defer u.wg.Done()
	buf := make([]byte, 65535)
	for {
		n, err := s.conn.Read(buf)
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}

			u.upstreamErrors.Add(1)
			u.evict(s, "upstream error")
			return
		}
		u.mu.Lock()
		if u.sessions[s.key] != s {

			u.mu.Unlock()
			return
		}
		if n == 0 && u.opt.DropZeroLength {
			u.mu.Unlock()
			continue
		}
		s.replied = true
		s.lastTX = u.opt.Now()
		u.mu.Unlock()

		u.s2cBytes.Add(int64(n))

		if _, err := u.conn.WriteToUDP(buf[:n], s.client); err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			u.sendDropped.Add(1)
		}
	}
}

func (u *udpListener) allowNewLocked() bool {
	now := u.opt.Now()
	elapsed := now.Sub(u.tokensAt).Seconds()
	u.tokens = min(u.opt.NewPerSec, u.tokens+elapsed*u.opt.NewPerSec)
	u.tokensAt = now
	if u.tokens < 1 {
		return false
	}
	u.tokens--
	return true
}

func (u *udpListener) evict(s *udpSession, reason string) {
	u.mu.Lock()
	if u.sessions[s.key] == s {
		delete(u.sessions, s.key)
		ip := s.client.IP.String()
		if n := u.perIP[ip] - 1; n <= 0 {
			delete(u.perIP, ip)
		} else {
			u.perIP[ip] = n
		}
	}
	u.mu.Unlock()
	s.closeOne.Do(func() {
		close(s.done)
		s.conn.Close()
	})
}

func (u *udpListener) reaper() {
	defer u.wg.Done()
	t := time.NewTicker(u.opt.ReapInterval)
	defer t.Stop()
	for {
		select {
		case <-u.done:
			return
		case <-t.C:
			u.ReapOnce()
		}
	}
}

// Unreplied sessions expire fast so random internet scanners cannot fill the table; real traffic gets the longer timer.
func (u *udpListener) ReapOnce() int {
	now := u.opt.Now()
	u.mu.Lock()
	var stale []*udpSession
	for _, s := range u.sessions {

		last := s.lastRX
		if s.lastTX.After(last) {
			last = s.lastTX
		}
		limit := u.opt.IdleNew
		if s.replied {
			limit = u.opt.IdleEstablished
		}
		if now.Sub(last) > limit {
			stale = append(stale, s)
		}
	}
	u.mu.Unlock()
	for _, s := range stale {
		u.evict(s, "idle")
	}
	return len(stale)
}

func (u *udpListener) Stop() {
	close(u.done)
	if u.conn != nil {
		u.conn.Close()
	}
	u.mu.Lock()
	u.closing = true
	sessions := make([]*udpSession, 0, len(u.sessions))
	for _, s := range u.sessions {
		sessions = append(sessions, s)
	}
	u.sessions = map[string]*udpSession{}
	u.perIP = map[string]int{}
	u.mu.Unlock()
	for _, s := range sessions {
		s.closeOne.Do(func() {
			close(s.done)
			s.conn.Close()
		})
	}

	u.wg.Wait()
}

func (u *udpListener) Stats() map[string]any {
	u.mu.Lock()
	live := len(u.sessions)
	u.mu.Unlock()
	return map[string]any{
		"proto": "udp", "bind": u.spec.bindAddr(), "target": u.spec.targetAddr(),
		"sessions":         live,
		"sessions_total":   u.total.Load(),
		"sessions_dropped": u.dropped.Load(),
		"send_dropped":     u.sendDropped.Load(),
		"upstream_errors":  u.upstreamErrors.Load(),
		"listener_errors":  u.listenerErrors.Load(),
		"open_failed":      u.openFailed.Load(),
		"c2s_bytes":        u.c2sBytes.Load(),
		"s2c_bytes":        u.s2cBytes.Load(),
	}
}

var _ = fmt.Sprintf
