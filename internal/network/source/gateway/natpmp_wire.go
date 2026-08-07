package gateway

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/sixxhands/fyrwire/internal/node/model"
)

// RFC 6886 NAT-PMP speaks raw UDP on port 5351. Twelve bytes out, sixteen bytes back, no XML in sight.
const (
	natpmpVersion  = 0
	opPublicAddr   = 0
	opMapUdp       = 1
	opMapTcp       = 2
	natpmpPort     = 5351
	natpmpRespFlag = 128
)

var natpmpResultText = map[uint16]string{
	0: "success",
	1: "unsupported protocol version",
	2: "not authorized (port forwarding may be disabled on the gateway)",
	3: "network failure at the gateway",
	4: "gateway out of resources",
	5: "unsupported opcode",
}

var natpmpPortForTest int

func (source *NatPmpSource) exchange(requestBytes []byte, wantLength int) ([]byte, error) {
	port := natpmpPort
	if natpmpPortForTest != 0 {
		port = natpmpPortForTest
	}

	address := &net.UDPAddr{IP: net.ParseIP(source.Gateway), Port: port}
	if address.IP == nil {
		return nil, model.Errorf("gateway %q is not an IP address", source.Gateway)
	}

	timeout := source.Timeout
	if timeout <= 0 {
		timeout = 3
	}

	retries := source.Retries
	if retries <= 0 {
		retries = 3
	}

	var lastErr error
	for attempt := 0; attempt < retries; attempt++ {
		wait := time.Duration(timeout * float64(int(1)<<attempt) * float64(time.Second))
		reply, err := sendNatPmpPacket(address, requestBytes, wantLength, wait)
		if err == nil {
			return reply, nil
		}
		lastErr = err
	}

	return nil, model.Errorf(
		"no NAT-PMP reply from %s:%d (%v); gateway may not speak NAT-PMP or port forwarding is off",
		source.Gateway, natpmpPort, lastErr,
	)
}

func sendNatPmpPacket(address *net.UDPAddr, requestBytes []byte, wantLength int, wait time.Duration) ([]byte, error) {
	conn, err := net.DialUDP("udp4", nil, address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(wait))
	if _, err := conn.Write(requestBytes); err != nil {
		return nil, err
	}

	buffer := make([]byte, 32)
	readCount, err := conn.Read(buffer)
	if err != nil {
		return nil, err
	}

	if readCount < wantLength {
		return nil, model.Errorf("short NAT-PMP reply (%d bytes)", readCount)
	}

	return buffer[:readCount], nil
}

func checkNatPmpResult(reply []byte, opcode byte) error {
	if reply[0] != natpmpVersion {
		return model.Errorf("NAT-PMP reply has version %d, expected 0", reply[0])
	}

	expectedOpcode := opcode + natpmpRespFlag
	if reply[1] != expectedOpcode {
		return model.Errorf("NAT-PMP reply opcode %d, expected %d", reply[1], expectedOpcode)
	}

	code := binary.BigEndian.Uint16(reply[2:4])
	if code == 0 {
		return nil
	}

	text, ok := natpmpResultText[code]
	if !ok {
		text = fmt.Sprintf("result code %d", code)
	}

	return model.Errorf("gateway refused: %s", text)
}

func (source *NatPmpSource) mapRawFull(internalPort int, protocol string) (gotInternal, external, granted int, err error) {
	opcode := byte(opMapTcp)
	if protocol == "udp" {
		opcode = opMapUdp
	}

	lifetime := source.Lifetime
	if lifetime <= 0 {
		lifetime = 120
	}

	packet := make([]byte, 12)
	packet[0] = natpmpVersion
	packet[1] = opcode
	binary.BigEndian.PutUint16(packet[4:6], uint16(internalPort))
	binary.BigEndian.PutUint16(packet[6:8], 0)
	binary.BigEndian.PutUint32(packet[8:12], uint32(lifetime))

	reply, err := source.exchange(packet, 16)
	if err != nil {
		return 0, 0, 0, err
	}

	if err := checkNatPmpResult(reply, opcode); err != nil {
		return 0, 0, 0, err
	}

	source.checkEpoch(binary.BigEndian.Uint32(reply[4:8]))

	return int(binary.BigEndian.Uint16(reply[8:10])),
		int(binary.BigEndian.Uint16(reply[10:12])),
		int(binary.BigEndian.Uint32(reply[12:16])),
		nil
}

func checkUnroutableAddress(address string) string {
	ip := net.ParseIP(address)
	if ip == nil {
		return "it is not a valid address"
	}
	if ip.IsUnspecified() {
		return "it is the unspecified address"
	}
	if ip.IsLoopback() {
		return "it is a loopback address"
	}
	if ip.IsPrivate() {
		return "it is a private address (RFC 1918)"
	}
	if ip.IsLinkLocalUnicast() {
		return "it is a link-local address"
	}
	if ipv4 := ip.To4(); ipv4 != nil && ipv4[0] == 100 && ipv4[1]&0xC0 == 64 {
		return "it is in the CGNAT range (100.64.0.0/10)"
	}

	return ""
}
