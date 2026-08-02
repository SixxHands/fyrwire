package source

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sixxhands/fyrwire/internal/model"
)

func fakeGateway(t *testing.T, handle func(req []byte) []byte) string {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 64)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if reply := handle(buf[:n]); reply != nil {
				conn.WriteToUDP(reply, addr)
			}
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).String()
}

func addrPort(t *testing.T, s string) (string, int) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		t.Fatal(err)
	}
	var p int
	if _, err := fmtSscan(port, &p); err != nil {
		t.Fatal(err)
	}
	return host, p
}

func fmtSscan(s string, p *int) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, model.Errorf("bad port %q", s)
		}
		n = n*10 + int(c-'0')
	}
	*p = n
	return 1, nil
}

func publicAddrReply(ip [4]byte) []byte {
	r := make([]byte, 12)
	r[0], r[1] = 0, opPublicAddr+natpmpRespFlag
	copy(r[8:12], ip[:])
	return r
}

func mapReply(op byte, internal, external uint16) []byte {
	r := make([]byte, 16)
	r[0], r[1] = 0, op+natpmpRespFlag
	binary.BigEndian.PutUint16(r[8:10], internal)
	binary.BigEndian.PutUint16(r[10:12], external)
	return r
}

func errorReply(op byte, code uint16) []byte {
	r := make([]byte, 16)
	r[0], r[1] = 0, op+natpmpRespFlag
	binary.BigEndian.PutUint16(r[2:4], code)
	return r
}

func TestTheGrantedPortIsUsedNotTheRequestedOne(t *testing.T) {
	addr := fakeGateway(t, func(req []byte) []byte {
		switch req[1] {
		case opPublicAddr:
			return publicAddrReply([4]byte{203, 0, 113, 9})
		case opMapTCP:
			return mapReply(opMapTCP, 25565, 51471)
		}
		return nil
	})
	host, port := addrPort(t, addr)
	n := &NatPmpSource{Gateway: host, Timeout: 1, Retries: 1}

	n.Gateway = host
	natpmpPortOverride(t, port)

	res, err := n.Resolve([]model.PortRequestData{
		{Key: "mc", InternalPort: 25565, Protocol: "tcp"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Host != "203.0.113.9" {
		t.Errorf("host = %q", res.Host)
	}
	if res.Ports["mc"] != 51471 {
		t.Errorf("port = %d, want the GRANTED 51471", res.Ports["mc"])
	}
}

func TestUdpAndTcpUseDifferentOpcodes(t *testing.T) {

	var mu sync.Mutex
	seen := map[byte]bool{}
	addr := fakeGateway(t, func(req []byte) []byte {
		mu.Lock()
		seen[req[1]] = true
		mu.Unlock()
		if req[1] == opPublicAddr {
			return publicAddrReply([4]byte{1, 2, 3, 4})
		}
		return mapReply(req[1], 1, 2)
	})
	host, port := addrPort(t, addr)
	natpmpPortOverride(t, port)
	n := &NatPmpSource{Gateway: host, Timeout: 1, Retries: 1}
	if _, err := n.Map(25565, "tcp"); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Map(2456, "udp"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !seen[opMapTCP] || !seen[opMapUDP] {
		t.Errorf("opcodes seen: %v — tcp and udp must differ", seen)
	}
}

func TestOneFailedMappingIsPartialNotFatal(t *testing.T) {
	addr := fakeGateway(t, func(req []byte) []byte {
		switch req[1] {
		case opPublicAddr:
			return publicAddrReply([4]byte{5, 6, 7, 8})
		case opMapTCP:
			if binary.BigEndian.Uint16(req[4:6]) == 9999 {
				return errorReply(opMapTCP, 4)
			}
			return mapReply(opMapTCP, 25565, 40000)
		}
		return nil
	})
	host, port := addrPort(t, addr)
	natpmpPortOverride(t, port)
	n := &NatPmpSource{Gateway: host, Timeout: 1, Retries: 1}
	res, err := n.Resolve([]model.PortRequestData{
		{Key: "ok", InternalPort: 25565, Protocol: "tcp"},
		{Key: "bad", InternalPort: 9999, Protocol: "tcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ports["ok"] != 40000 {
		t.Errorf("the working service must still publish: %v", res.Ports)
	}
	if !res.Partial["bad"] {
		t.Error("the failed service must be marked partial, not dropped silently")
	}
}

func TestAGatewayRefusalNamesTheLikelyCause(t *testing.T) {
	addr := fakeGateway(t, func(req []byte) []byte {
		return errorReply(req[1], 2)
	})
	host, port := addrPort(t, addr)
	natpmpPortOverride(t, port)
	n := &NatPmpSource{Gateway: host, Timeout: 1, Retries: 1}
	_, err := n.PublicAddress()
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !contains(err.Error(), "port forwarding") {
		t.Errorf("a bare result code is not actionable: %v", err)
	}
}

func TestNoReplyIsAnActionableMessage(t *testing.T) {
	n := &NatPmpSource{Gateway: "127.0.0.1", Timeout: 0.05, Retries: 1}
	natpmpPortOverride(t, 1)
	_, err := n.PublicAddress()
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if !contains(err.Error(), "NAT-PMP") {
		t.Errorf("message = %v", err)
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func natpmpPortOverride(t *testing.T, port int) {
	prev := natpmpPortForTest
	natpmpPortForTest = port
	t.Cleanup(func() { natpmpPortForTest = prev })
}

func TestAnOutOfRangePortIsRefusedNotWrapped(t *testing.T) {
	addr := fakeGateway(t, func(req []byte) []byte {
		return mapReply(req[1], 1, 2)
	})
	host, port := addrPort(t, addr)
	natpmpPortOverride(t, port)
	n := &NatPmpSource{Gateway: host, Timeout: 1, Retries: 1}
	for _, bad := range []int{0, -1, 65536, 70000} {
		if _, err := n.Map(bad, "tcp"); err == nil {
			t.Errorf("port %d was accepted", bad)
		}
	}
}

func TestAGrantOfPortZeroIsNotAMapping(t *testing.T) {
	addr := fakeGateway(t, func(req []byte) []byte {
		if req[1] == opPublicAddr {
			return publicAddrReply([4]byte{1, 2, 3, 4})
		}
		return mapReply(req[1], 25565, 0)
	})
	host, port := addrPort(t, addr)
	natpmpPortOverride(t, port)
	n := &NatPmpSource{Gateway: host, Timeout: 1, Retries: 1}
	if _, err := n.Map(25565, "tcp"); err == nil {
		t.Error("port 0 was accepted as a granted mapping")
	}

	res, err := n.Resolve([]model.PortRequestData{
		{Key: "mc", InternalPort: 25565, Protocol: "tcp"}})
	if err == nil && !res.Partial["mc"] {
		t.Errorf("a zero grant must be partial, got %+v", res)
	}
}

func TestAFractionalTimeoutIsNotTruncatedToZero(t *testing.T) {
	addr := fakeGateway(t, func(req []byte) []byte {
		time.Sleep(120 * time.Millisecond)
		return publicAddrReply([4]byte{9, 9, 9, 9})
	})
	host, port := addrPort(t, addr)
	natpmpPortOverride(t, port)
	n := &NatPmpSource{Gateway: host, Timeout: 0.5, Retries: 1}
	got, err := n.PublicAddress()
	if err != nil {
		t.Fatalf("a 0.5s timeout must wait half a second, not zero: %v", err)
	}
	if got != "9.9.9.9" {
		t.Errorf("got %q", got)
	}
}
