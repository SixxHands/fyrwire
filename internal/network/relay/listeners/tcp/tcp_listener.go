package tcp

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

type Listener struct {
	key        string
	bindAddr   string
	targetAddr string
	log        func(string)
	ln         net.Listener
	done       chan struct{}
	wg         sync.WaitGroup
	sem        chan struct{}
	mu         sync.Mutex
	active     int
	total      int
	refused    int
	connTime   time.Duration
	conns      map[net.Conn]struct{}
	closing    bool
}

const (
	tcpMaxConns       = 64
	tcpConnectTimeout = 5 * time.Second
	tcpBufSize        = 64 * 1024
)

func (listener *Listener) Start() error {
	ln, err := net.Listen("tcp", listener.bindAddr)
	if err != nil {
		return err
	}

	listener.ln = ln
	listener.wg.Add(1)
	go listener.acceptLoop()
	return nil
}

func (listener *Listener) acceptLoop() {
	defer listener.wg.Done()
	for {
		conn, err := listener.ln.Accept()
		if err != nil {
			if listener.shouldStopAccept(err) {
				return
			}
			continue
		}

		if !listener.acquireConnection(conn) {
			continue
		}

		listener.wg.Add(1)
		go listener.serveConnection(conn)
	}
}

func (listener *Listener) shouldStopAccept(err error) bool {
	select {
	case <-listener.done:
		return true
	default:
	}

	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return false
	}

	if errors.Is(err, net.ErrClosed) {
		return true
	}

	listener.log(fmt.Sprintf("relay %s: accept error, retrying: %v", listener.key, err))
	time.Sleep(100 * time.Millisecond)
	return false
}

func (listener *Listener) acquireConnection(conn net.Conn) bool {
	select {
	case listener.sem <- struct{}{}:
		listener.mu.Lock()
		listener.active++
		listener.total++
		listener.mu.Unlock()
		return true
	default:
		listener.mu.Lock()
		listener.refused++
		listener.mu.Unlock()
		conn.Close()
		return false
	}
}

func (listener *Listener) serveConnection(client net.Conn) {
	defer listener.wg.Done()
	defer func() {
		<-listener.sem
		listener.mu.Lock()
		listener.active--
		delete(listener.conns, client)
		listener.mu.Unlock()
		client.Close()
	}()

	backend, err := net.DialTimeout("tcp", listener.targetAddr, listener.connTime)
	if err != nil {
		return
	}
	defer backend.Close()

	listener.mu.Lock()
	if listener.closing {
		listener.mu.Unlock()
		return
	}
	listener.conns[client] = struct{}{}
	listener.conns[backend] = struct{}{}
	listener.mu.Unlock()

	defer func() {
		listener.mu.Lock()
		delete(listener.conns, backend)
		listener.mu.Unlock()
	}()

	setNoDelay(client)
	setNoDelay(backend)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pump(backend, client) }()
	go func() { defer wg.Done(); pump(client, backend) }()
	wg.Wait()
}

func setNoDelay(conn net.Conn) {
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
	}
}

func pump(dst, src net.Conn) {
	buffer := make([]byte, tcpBufSize)
	for {
		count, err := src.Read(buffer)
		if count > 0 {
			if _, writeErr := dst.Write(buffer[:count]); writeErr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}

	if tcpConn, ok := dst.(*net.TCPConn); ok {
		_ = tcpConn.CloseWrite()
	}
}

// Close the listener and kick every open socket so port rotation never deadlocks (deadlock mentioned?????) waiting on an idle player.
func (listener *Listener) Stop() {
	close(listener.done)
	if listener.ln != nil {
		listener.ln.Close()
	}

	listener.mu.Lock()
	listener.closing = true
	for conn := range listener.conns {
		conn.Close()
	}
	listener.conns = map[net.Conn]struct{}{}
	listener.mu.Unlock()

	listener.wg.Wait()
}

func (listener *Listener) Stats() map[string]any {
	listener.mu.Lock()
	defer listener.mu.Unlock()

	return map[string]any{
		"proto":   "tcp",
		"bind":    listener.bindAddr,
		"target":  listener.targetAddr,
		"active":  listener.active,
		"total":   listener.total,
		"refused": listener.refused,
	}
}
