package gateway

import (
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sixxhands/fyrwire/internal/node/model"
)

func fakeGateway(t *testing.T, respond func(req []byte) []byte) string {
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
			if reply := respond(buf[:n]); reply != nil {
				conn.WriteToUDP(reply, addr)
			}
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).String()
}

func addrPort(t *testing.T, s string) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(s)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return host, p
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

func natpmpPortOverride(t *testing.T, port int) {
	prev := natpmpPortForTest
	natpmpPortForTest = port
	t.Cleanup(func() { natpmpPortForTest = prev })
}

func startNatPmpSource(t *testing.T, respond func(req []byte) []byte) *NatPmpSource {
	t.Helper()
	host, port := addrPort(t, fakeGateway(t, respond))
	natpmpPortOverride(t, port)
	return &NatPmpSource{Gateway: host, Timeout: 1, Retries: 1}
}

func Test_granted_port_is_used_not_requested(t *testing.T) {
	n := startNatPmpSource(t, func(req []byte) []byte {
		switch req[1] {
		case opPublicAddr:
			return publicAddrReply([4]byte{203, 0, 113, 9})
		case opMapTcp:
			return mapReply(opMapTcp, 25565, 51471)
		}
		return nil
	})

	res, err := n.FindEndpoints([]model.PortRequestData{
		{Key: "mc", InternalPort: 25565, Protocol: "tcp"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Host != "203.0.113.9" {
		t.Errorf("host = %q", res.Host)
	}
	if res.Ports["mc"] != 51471 {
		t.Errorf("port = %d, want 51471", res.Ports["mc"])
	}
}

func Test_udp_and_tcp_use_different_opcodes(t *testing.T) {
	var mu sync.Mutex
	seen := map[byte]bool{}
	n := startNatPmpSource(t, func(req []byte) []byte {
		mu.Lock()
		seen[req[1]] = true
		mu.Unlock()
		if req[1] == opPublicAddr {
			return publicAddrReply([4]byte{1, 2, 3, 4})
		}
		return mapReply(req[1], binary.BigEndian.Uint16(req[4:6]), 2)
	})
	if _, err := n.Map(25565, "tcp"); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Map(2456, "udp"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !seen[opMapTcp] || !seen[opMapUdp] {
		t.Errorf("opcodes seen: %v", seen)
	}
}

func Test_one_failed_mapping_is_partial(t *testing.T) {
	n := startNatPmpSource(t, func(req []byte) []byte {
		switch req[1] {
		case opPublicAddr:
			return publicAddrReply([4]byte{5, 6, 7, 8})
		case opMapTcp:
			if binary.BigEndian.Uint16(req[4:6]) == 9999 {
				return errorReply(opMapTcp, 4)
			}
			return mapReply(opMapTcp, 25565, 40000)
		}
		return nil
	})
	res, err := n.FindEndpoints([]model.PortRequestData{
		{Key: "ok", InternalPort: 25565, Protocol: "tcp"},
		{Key: "bad", InternalPort: 9999, Protocol: "tcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ports["ok"] != 40000 {
		t.Errorf("ports = %v", res.Ports)
	}
	if !res.Partial["bad"] {
		t.Error("failed service must be marked partial")
	}
}

func Test_gateway_refusal_mentions_port_forwarding(t *testing.T) {
	n := startNatPmpSource(t, func(req []byte) []byte { return errorReply(req[1], 2) })
	_, err := n.PublicAddress()
	if err == nil || !strings.Contains(err.Error(), "port forwarding") {
		t.Errorf("err = %v", err)
	}
}

func Test_no_reply_mentions_nat_pmp(t *testing.T) {
	n := &NatPmpSource{Gateway: "127.0.0.1", Timeout: 0.05, Retries: 1}
	natpmpPortOverride(t, 1)
	_, err := n.PublicAddress()
	if err == nil || !strings.Contains(err.Error(), "NAT-PMP") {
		t.Errorf("err = %v", err)
	}
}

func Test_out_of_range_port_is_refused(t *testing.T) {
	n := startNatPmpSource(t, func(req []byte) []byte { return mapReply(req[1], 1, 2) })
	for _, bad := range []int{0, -1, 65536, 70000} {
		if _, err := n.Map(bad, "tcp"); err == nil {
			t.Errorf("port %d was accepted", bad)
		}
	}
}

func Test_port_zero_grant_is_rejected(t *testing.T) {
	n := startNatPmpSource(t, func(req []byte) []byte {
		if req[1] == opPublicAddr {
			return publicAddrReply([4]byte{1, 2, 3, 4})
		}
		return mapReply(req[1], 25565, 0)
	})
	if _, err := n.Map(25565, "tcp"); err == nil {
		t.Error("port 0 was accepted")
	}
	res, err := n.FindEndpoints([]model.PortRequestData{
		{Key: "mc", InternalPort: 25565, Protocol: "tcp"}})
	if err == nil && !res.Partial["mc"] {
		t.Errorf("zero grant must be partial: %+v", res)
	}
}

func Test_fractional_timeout_is_not_truncated(t *testing.T) {
	n := startNatPmpSource(t, func(req []byte) []byte {
		time.Sleep(120 * time.Millisecond)
		return publicAddrReply([4]byte{9, 9, 9, 9})
	})
	n.Timeout = 0.5
	got, err := n.PublicAddress()
	if err != nil {
		t.Fatalf("0.5s timeout failed: %v", err)
	}
	if got != "9.9.9.9" {
		t.Errorf("got %q", got)
	}
}

func Test_substituted_internal_port_is_detected_as_single_port(t *testing.T) {
	addr := fakeGateway(t, func(req []byte) []byte {
		if req[1] == opPublicAddr {
			return publicAddrReply([4]byte{203, 0, 113, 49})
		}
		internal := binary.BigEndian.Uint16(req[4:6])
		if internal == 0 {
			return mapReply(req[1], 52329, 52329)
		}
		return mapReply(req[1], internal, 52707)
	})
	host, port := addrPort(t, addr)
	natpmpPortOverride(t, port)
	n := &NatPmpSource{Gateway: host, Timeout: 1, Retries: 1}

	got, single := n.SinglePort("tcp")
	if !single || got != 52329 {
		t.Fatalf("SinglePort = (%d, %v), want (52329, true)", got, single)
	}
	res, err := n.FindEndpoints([]model.PortRequestData{
		{Key: "mc", InternalPort: 37772, Protocol: "tcp"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ports["mc"] != 52329 {
		t.Errorf("published %d, want 52329", res.Ports["mc"])
	}
}

func Test_conforming_gateway_gets_per_service_mappings(t *testing.T) {
	addr := fakeGateway(t, func(req []byte) []byte {
		if req[1] == opPublicAddr {
			return publicAddrReply([4]byte{1, 2, 3, 4})
		}
		internal := binary.BigEndian.Uint16(req[4:6])
		if internal == 0 {
			return errorReply(req[1], 2)
		}
		return mapReply(req[1], internal, internal+10000)
	})
	host, port := addrPort(t, addr)
	natpmpPortOverride(t, port)
	n := &NatPmpSource{Gateway: host, Timeout: 1, Retries: 1}
	if _, single := n.SinglePort("tcp"); single {
		t.Error("conforming gateway must not be treated as single-port")
	}
	res, err := n.FindEndpoints([]model.PortRequestData{
		{Key: "mc", InternalPort: 25565, Protocol: "tcp"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ports["mc"] != 35565 {
		t.Errorf("got %d, want 35565", res.Ports["mc"])
	}
}

func Test_services_share_port_on_single_port_gateway(t *testing.T) {
	addr := fakeGateway(t, func(req []byte) []byte {
		if req[1] == opPublicAddr {
			return publicAddrReply([4]byte{1, 2, 3, 4})
		}
		if binary.BigEndian.Uint16(req[4:6]) == 0 {
			return mapReply(req[1], 52329, 52329)
		}
		return mapReply(req[1], binary.BigEndian.Uint16(req[4:6]), 9999)
	})
	host, port := addrPort(t, addr)
	natpmpPortOverride(t, port)
	var warnings []string
	n := &NatPmpSource{Gateway: host, Timeout: 1, Retries: 1,
		Log: func(s string) { warnings = append(warnings, s) }}
	res, err := n.FindEndpoints([]model.PortRequestData{
		{Key: "mc", InternalPort: 25565, Protocol: "tcp"},
		{Key: "vh", InternalPort: 2456, Protocol: "tcp"}})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range res.Ports {
		if v != 52329 {
			t.Errorf("%s got %d, want 52329", k, v)
		}
	}
	if len(warnings) == 0 {
		t.Error("expected warning when sharing a single port")
	}
}

func Test_mismatched_internal_port_reply_is_rejected(t *testing.T) {
	addr := fakeGateway(t, func(req []byte) []byte {
		if req[1] == opPublicAddr {
			return publicAddrReply([4]byte{1, 2, 3, 4})
		}
		internal := binary.BigEndian.Uint16(req[4:6])
		if internal == 0 {
			return errorReply(req[1], 2)
		}
		return mapReply(req[1], internal+1, 52707)
	})
	host, port := addrPort(t, addr)
	natpmpPortOverride(t, port)
	n := &NatPmpSource{Gateway: host, Timeout: 1, Retries: 1}
	if _, err := n.Map(25565, "tcp"); err == nil {
		t.Error("reply for a different internal port must be rejected")
	}
}

func Test_granted_lease_is_recorded_and_warns_when_short(t *testing.T) {
	addr := fakeGateway(t, func(req []byte) []byte {
		if req[1] == opPublicAddr {
			return publicAddrReply([4]byte{1, 2, 3, 4})
		}
		r := mapReply(req[1], binary.BigEndian.Uint16(req[4:6]), 40000)
		binary.BigEndian.PutUint32(r[12:16], 60)
		return r
	})
	host, port := addrPort(t, addr)
	natpmpPortOverride(t, port)
	var warnings []string
	n := &NatPmpSource{Gateway: host, Lifetime: 120, Timeout: 1, Retries: 1,
		Log: func(s string) { warnings = append(warnings, s) }}
	if _, err := n.Map(25565, "tcp"); err != nil {
		t.Fatal(err)
	}
	if got := n.GrantedLifetime(); got != 60 {
		t.Errorf("GrantedLifetime = %d, want 60", got)
	}
	if len(warnings) == 0 {
		t.Error("expected warning for short lease")
	}
}

func Test_gateway_restart_is_detected(t *testing.T) {
	var epoch atomic.Uint32
	epoch.Store(5000)
	addr := fakeGateway(t, func(req []byte) []byte {
		var r []byte
		if req[1] == opPublicAddr {
			r = publicAddrReply([4]byte{1, 2, 3, 4})
		} else {
			r = mapReply(req[1], binary.BigEndian.Uint16(req[4:6]), 40000)
		}
		binary.BigEndian.PutUint32(r[4:8], epoch.Load())
		return r
	})
	host, port := addrPort(t, addr)
	natpmpPortOverride(t, port)
	var warnings []string
	n := &NatPmpSource{Gateway: host, Lifetime: 120, Timeout: 1, Retries: 1,
		Log: func(s string) { warnings = append(warnings, s) }}

	n.Map(25565, "tcp")
	if n.TookRestart() {
		t.Fatal("unexpected restart flag")
	}
	epoch.Store(6000)
	n.Map(25565, "tcp")
	if n.TookRestart() {
		t.Fatal("rising epoch is not a restart")
	}
	epoch.Store(12)
	n.Map(25565, "tcp")
	if !n.TookRestart() {
		t.Fatal("backwards epoch must trigger restart")
	}
	if n.TookRestart() {
		t.Error("restart flag must clear after read")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "restarted") {
			found = true
		}
	}
	if !found {
		t.Errorf("restart warning missing: %v", warnings)
	}
}
