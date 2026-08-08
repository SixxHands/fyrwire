package udp

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type udpSession struct {
	client   *net.UDPAddr
	key      string
	conn     *net.UDPConn
	lastRx   time.Time
	lastTx   time.Time
	replied  bool
	done     chan struct{}
	closeOne sync.Once
}

func (session *udpSession) close() {
	session.closeOne.Do(func() {
		close(session.done)
		session.conn.Close()
	})
}

type Listener struct {
	key        string
	bindAddr   string
	targetAddr string
	log        func(string)
	options    OptionsData
	conn       *net.UDPConn
	backend    *net.UDPAddr
	done       chan struct{}
	wg         sync.WaitGroup
	mu         sync.Mutex
	closing    bool
	sessions   map[string]*udpSession
	perIp      map[string]int
	tokens     float64
	tokensAt   time.Time

	total          atomic.Int64
	dropped        atomic.Int64
	sendDropped    atomic.Int64
	upstreamErrors atomic.Int64
	listenerErrors atomic.Int64
	openFailed     atomic.Int64
	c2sBytes       atomic.Int64
	s2cBytes       atomic.Int64
}

func (listener *Listener) Start() error {
	backend, err := net.ResolveUDPAddr("udp4", listener.targetAddr)
	if err != nil {
		return err
	}

	address, err := net.ResolveUDPAddr("udp4", listener.bindAddr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp4", address)
	if err != nil {
		return err
	}

	listener.backend = backend
	listener.conn = conn
	listener.wg.Add(2)
	go listener.readClients()
	go listener.reaper()
	return nil
}

func (listener *Listener) readClients() {
	defer listener.wg.Done()
	buffer := make([]byte, 65535)

	for {
		count, clientAddress, err := listener.conn.ReadFromUDP(buffer)
		if err != nil {
			if listener.isStopped(err) {
				return
			}
			listener.listenerErrors.Add(1)
			continue
		}

		listener.onClient(buffer[:count], clientAddress)
	}
}

func (listener *Listener) isStopped(err error) bool {
	select {
	case <-listener.done:
		return true
	default:
	}

	return errors.Is(err, net.ErrClosed)
}

func (listener *Listener) onClient(data []byte, clientAddress *net.UDPAddr) {
	if len(data) == 0 && listener.options.DropZeroLength {
		return
	}

	conn := listener.getOrOpenSession(clientAddress)
	if conn == nil {
		return
	}

	listener.c2sBytes.Add(int64(len(data)))
	_ = conn.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := conn.Write(data); err != nil {
		listener.sendDropped.Add(1)
	}
}

func (listener *Listener) getOrOpenSession(clientAddress *net.UDPAddr) *net.UDPConn {
	key := clientAddress.String()
	listener.mu.Lock()
	defer listener.mu.Unlock()

	if listener.closing {
		listener.dropped.Add(1)
		return nil
	}

	session, ok := listener.sessions[key]
	if !ok {
		if !listener.canOpenSessionLocked(clientAddress) {
			listener.dropped.Add(1)
			return nil
		}

		var err error
		session, err = listener.openSessionLocked(clientAddress, key)
		if err != nil {
			listener.openFailed.Add(1)
			return nil
		}
	}

	session.lastRx = listener.options.Now()
	return session.conn
}

func (listener *Listener) canOpenSessionLocked(clientAddress *net.UDPAddr) bool {
	if len(listener.sessions) >= listener.options.MaxSessions {
		return false
	}
	if listener.perIp[clientAddress.IP.String()] >= listener.options.MaxPerIp {
		return false
	}

	return listener.allowNewLocked()
}

func (listener *Listener) openSessionLocked(clientAddress *net.UDPAddr, key string) (*udpSession, error) {
	conn, err := net.DialUDP("udp4", nil, listener.backend)
	if err != nil {
		return nil, err
	}

	now := listener.options.Now()
	session := &udpSession{
		client: clientAddress, key: key, conn: conn,
		lastRx: now, lastTx: now, done: make(chan struct{}),
	}

	listener.sessions[key] = session
	listener.perIp[clientAddress.IP.String()]++
	listener.total.Add(1)
	listener.wg.Add(1)
	go listener.readBackend(session)
	return session, nil
}

func (listener *Listener) readBackend(session *udpSession) {
	defer listener.wg.Done()
	buffer := make([]byte, 65535)

	for {
		count, err := session.conn.Read(buffer)
		if err != nil {
			listener.onBackendReadError(session)
			return
		}
		if !listener.markBackendReply(session, count) {
			return
		}
		if count == 0 && listener.options.DropZeroLength {
			continue
		}

		listener.s2cBytes.Add(int64(count))
		if _, err := listener.conn.WriteToUDP(buffer[:count], session.client); err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			listener.sendDropped.Add(1)
		}
	}
}

func (listener *Listener) onBackendReadError(session *udpSession) {
	select {
	case <-session.done:
		return
	default:
	}

	listener.upstreamErrors.Add(1)
	listener.evict(session)
}

func (listener *Listener) markBackendReply(session *udpSession, count int) bool {
	listener.mu.Lock()
	defer listener.mu.Unlock()

	if listener.sessions[session.key] != session {
		return false
	}
	if count == 0 && listener.options.DropZeroLength {
		return true
	}

	session.replied = true
	session.lastTx = listener.options.Now()
	return true
}

func (listener *Listener) allowNewLocked() bool {
	now := listener.options.Now()
	elapsed := now.Sub(listener.tokensAt).Seconds()
	listener.tokens = min(listener.options.NewPerSec, listener.tokens+elapsed*listener.options.NewPerSec)
	listener.tokensAt = now

	if listener.tokens < 1 {
		return false
	}

	listener.tokens--
	return true
}

func (listener *Listener) evict(session *udpSession) {
	listener.mu.Lock()
	if listener.sessions[session.key] == session {
		delete(listener.sessions, session.key)
		ip := session.client.IP.String()
		if remaining := listener.perIp[ip] - 1; remaining <= 0 {
			delete(listener.perIp, ip)
		} else {
			listener.perIp[ip] = remaining
		}
	}
	listener.mu.Unlock()

	session.close()
}

func (listener *Listener) clearSessions() []*udpSession {
	listener.mu.Lock()
	defer listener.mu.Unlock()

	listener.closing = true
	active := make([]*udpSession, 0, len(listener.sessions))
	for _, session := range listener.sessions {
		active = append(active, session)
	}

	listener.sessions = map[string]*udpSession{}
	listener.perIp = map[string]int{}
	return active
}

func (listener *Listener) reaper() {
	defer listener.wg.Done()
	ticker := time.NewTicker(listener.options.ReapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-listener.done:
			return
		case <-ticker.C:
			listener.ReapOnce()
		}
	}
}

// Unreplied sessions expire fast so random internet scanners cannot fill the table; real traffic gets the longer timer.
func (listener *Listener) ReapOnce() int {
	stale := listener.findStaleSessions(listener.options.Now())
	for _, session := range stale {
		listener.evict(session)
	}

	return len(stale)
}

func (listener *Listener) findStaleSessions(now time.Time) []*udpSession {
	listener.mu.Lock()
	defer listener.mu.Unlock()

	var stale []*udpSession
	for _, session := range listener.sessions {
		lastActive := session.lastRx
		if session.lastTx.After(lastActive) {
			lastActive = session.lastTx
		}

		limit := listener.options.IdleNew
		if session.replied {
			limit = listener.options.IdleEstablished
		}

		if now.Sub(lastActive) > limit {
			stale = append(stale, session)
		}
	}

	return stale
}

func (listener *Listener) Stop() {
	close(listener.done)
	if listener.conn != nil {
		listener.conn.Close()
	}

	for _, session := range listener.clearSessions() {
		session.close()
	}

	listener.wg.Wait()
}

func (listener *Listener) Stats() map[string]any {
	listener.mu.Lock()
	live := len(listener.sessions)
	listener.mu.Unlock()

	return map[string]any{
		"proto": "udp", "bind": listener.bindAddr, "target": listener.targetAddr,
		"sessions": live, "sessions_total": listener.total.Load(),
		"sessions_dropped": listener.dropped.Load(), "send_dropped": listener.sendDropped.Load(),
		"upstream_errors": listener.upstreamErrors.Load(), "listener_errors": listener.listenerErrors.Load(),
		"open_failed": listener.openFailed.Load(), "c2s_bytes": listener.c2sBytes.Load(),
		"s2c_bytes": listener.s2cBytes.Load(),
	}
}
