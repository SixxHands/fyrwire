package source

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/sixxhands/fyrwire/internal/config"
	"github.com/sixxhands/fyrwire/internal/model"
)

// RFC 6886 NAT-PMP speaks raw UDP on port 5351. Twelve bytes out, sixteen bytes back, no XML in sight.
const (
	natpmpVersion  = 0
	opPublicAddr   = 0
	opMapUDP       = 1
	opMapTCP       = 2
	natpmpPort     = 5351
	natpmpRespFlag = 128
)

var natpmpResultText = map[uint16]string{
	0: "success",
	1: "unsupported protocol version",
	2: "not authorised — port forwarding is probably disabled on the gateway",
	3: "network failure at the gateway",
	4: "the gateway is out of resources",
	5: "unsupported opcode",
}

var natpmpPortForTest int

type NatPmpSource struct {
	Gateway  string
	Lifetime int
	Timeout  float64
	Retries  int

	Log func(string)

	Restarted bool

	lastEpoch uint32
	granted   int
	warned    map[string]bool
}

func (n *NatPmpSource) warnOnce(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if n.warned == nil {
		n.warned = map[string]bool{}
	}
	if n.warned[msg] {
		return
	}
	n.warned[msg] = true
	if n.Log != nil {
		n.Log(msg)
	}
}

func (n *NatPmpSource) Name() string { return "natpmp" }

func (n *NatPmpSource) exchange(payload []byte, wantLen int) ([]byte, error) {
	port := natpmpPort
	if natpmpPortForTest != 0 {
		port = natpmpPortForTest
	}
	addr := &net.UDPAddr{IP: net.ParseIP(n.Gateway), Port: port}
	if addr.IP == nil {
		return nil, model.Errorf("gateway %q is not an IP address", n.Gateway)
	}
	timeout := n.Timeout
	if timeout <= 0 {
		timeout = 3
	}
	retries := n.Retries
	if retries <= 0 {
		retries = 3
	}
	var lastErr error
	for attempt := 0; attempt < retries; attempt++ {
		conn, err := net.DialUDP("udp4", nil, addr)
		if err != nil {
			lastErr = err
			continue
		}

		wait := time.Duration(timeout * float64(int(1)<<attempt) * float64(time.Second))
		_ = conn.SetDeadline(time.Now().Add(wait))
		if _, err := conn.Write(payload); err != nil {
			conn.Close()
			lastErr = err
			continue
		}
		buf := make([]byte, 32)
		nRead, err := conn.Read(buf)
		conn.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if nRead < wantLen {
			lastErr = model.Errorf("short NAT-PMP reply (%d bytes)", nRead)
			continue
		}
		return buf[:nRead], nil
	}
	return nil, model.Errorf(
		"no NAT-PMP reply from %s:%d — %v. The gateway may not speak NAT-PMP, "+
			"or port forwarding may be off.", n.Gateway, natpmpPort, lastErr)
}

func checkResult(reply []byte, opcode byte) error {
	if reply[0] != natpmpVersion {
		return model.Errorf("NAT-PMP reply has version %d, expected 0", reply[0])
	}
	if want := opcode + natpmpRespFlag; reply[1] != want {
		return model.Errorf("NAT-PMP reply opcode %d, expected %d", reply[1], want)
	}
	code := binary.BigEndian.Uint16(reply[2:4])
	if code != 0 {
		text, ok := natpmpResultText[code]
		if !ok {
			text = fmt.Sprintf("result code %d", code)
		}
		return model.Errorf("gateway refused: %s", text)
	}
	return nil
}

func (n *NatPmpSource) PublicAddress() (string, error) {
	reply, err := n.exchange([]byte{natpmpVersion, opPublicAddr}, 12)
	if err != nil {
		return "", err
	}
	if err := checkResult(reply, opPublicAddr); err != nil {
		return "", err
	}
	return net.IP(reply[8:12]).String(), nil
}

func (n *NatPmpSource) mapRaw(internal int, protocol string) (gotInternal, external int, err error) {
	gotInternal, external, _, err = n.mapRawFull(internal, protocol)
	return
}

func (n *NatPmpSource) mapRawFull(internal int, protocol string) (
	gotInternal, external, granted int, err error) {
	op := byte(opMapTCP)
	if protocol == "udp" {
		op = opMapUDP
	}
	lifetime := n.Lifetime
	if lifetime <= 0 {
		lifetime = 120
	}
	req := make([]byte, 12)
	req[0], req[1] = natpmpVersion, op
	binary.BigEndian.PutUint16(req[4:6], uint16(internal))
	binary.BigEndian.PutUint16(req[6:8], 0)
	binary.BigEndian.PutUint32(req[8:12], uint32(lifetime))

	reply, err := n.exchange(req, 16)
	if err != nil {
		return 0, 0, 0, err
	}
	if err := checkResult(reply, op); err != nil {
		return 0, 0, 0, err
	}

	n.checkEpoch(binary.BigEndian.Uint32(reply[4:8]))

	return int(binary.BigEndian.Uint16(reply[8:10])),
		int(binary.BigEndian.Uint16(reply[10:12])),
		int(binary.BigEndian.Uint32(reply[12:16])), nil
}

func (n *NatPmpSource) checkEpoch(epoch uint32) {
	prev := n.lastEpoch
	n.lastEpoch = epoch
	if prev != 0 && epoch < prev {

		n.Restarted = true
		if n.Log != nil {
			n.Log(fmt.Sprintf(
				"the gateway restarted (epoch %d -> %d); every port mapping was "+
					"lost, so all of them are being re-created", prev, epoch))
		}
	}
}

func (n *NatPmpSource) TookRestart() bool {
	was := n.Restarted
	n.Restarted = false
	return was
}

func (n *NatPmpSource) GrantedLifetime() int { return n.granted }

func (n *NatPmpSource) noteLifetime(secs int) {
	if secs <= 0 {
		return
	}
	if n.granted == 0 || secs < n.granted {
		n.granted = secs
	}
	if n.Lifetime > 0 && secs < n.Lifetime {
		n.warnOnce("asked the gateway for a %ds port lease and it granted %ds — "+
			"the agent must reconcile more often than that or the mapping lapses",
			n.Lifetime, secs)
	}
}

// Some VPNs (looking at you, Proton) hand out one port per session and ignore whatever internal port you asked for. Asking for 0 catches them.
func (n *NatPmpSource) SinglePort(protocol string) (port int, isSingle bool) {
	gotInternal, external, granted, err := n.mapRawFull(0, protocol)
	if err != nil {
		return 0, false
	}
	n.noteLifetime(granted)
	if gotInternal != 0 && gotInternal == external && external > 0 {
		return external, true
	}
	return 0, false
}

func (n *NatPmpSource) Map(internal int, protocol string) (int, error) {
	if internal < 1 || internal > 65535 {

		return 0, model.Errorf(
			"internal port %d is out of range — a port is 1..65535", internal)
	}
	gotInternal, granted, lease, err := n.mapRawFull(internal, protocol)
	if err != nil {
		return 0, err
	}
	n.noteLifetime(lease)
	if gotInternal != internal {

		return 0, model.Errorf(
			"asked to map internal port %d but the gateway answered with "+
				"internal port %d — this gateway assigns one port per session "+
				"rather than one per service", internal, gotInternal)
	}
	if granted == 0 {

		return 0, model.Errorf(
			"the gateway reported success but granted port 0, which is not a " +
				"usable mapping")
	}
	return granted, nil
}

func (n *NatPmpSource) Resolve(reqs []model.PortRequestData) (model.EndpointData, error) {
	host, err := n.PublicAddress()
	if err != nil {
		return model.EndpointData{}, err
	}
	res := model.EndpointData{Host: host, Ports: map[string]int{},
		Partial: map[string]bool{}}

	if len(reqs) > 0 {

		byProto := map[string]bool{}
		for _, r := range reqs {
			byProto[r.Protocol] = true
		}
		granted := map[string]int{}
		anySingle := false
		for proto := range byProto {
			if port, single := n.SinglePort(proto); single {
				granted[proto] = port
				anySingle = true
			}
		}
		if anySingle {
			for _, r := range reqs {
				port, ok := granted[r.Protocol]
				if !ok {

					res.Partial[r.Key] = true
					continue
				}
				res.Ports[r.Key] = port
			}
			if len(byProto) > 1 {
				n.warnOnce("this gateway grants one port per protocol; tcp and " +
					"udp services do not share a mapping request")
			}
			if len(reqs) > len(byProto) {
				n.warnOnce("this gateway grants ONE port per session, so " +
					"services sharing a protocol share a port — only the one " +
					"your relay targets will actually work")
			}
			return res, nil
		}
		if false {
			for _, r := range reqs {
				_ = r
			}
		}
	}

	for _, r := range reqs {
		port, err := n.Map(r.InternalPort, r.Protocol)
		if err != nil {

			res.Partial[r.Key] = true
			continue
		}
		res.Ports[r.Key] = port
	}
	if len(res.Ports) == 0 && len(reqs) > 0 {
		return model.EndpointData{}, model.Errorf(
			"the gateway granted no ports at all — check that port forwarding " +
				"is enabled")
	}
	return res, nil
}

func init() {
	register("natpmp", []config.OptionRule{
		{Name: "gateway", Required: true, Kind: "string"},
		{Name: "lifetime", Kind: "int"},
		{Name: "timeout", Kind: "float"},
		{Name: "retries", Kind: "int"},
	}, func(sec config.Section) (IPortSource, error) {
		gw, err := sec.String("gateway")
		if err != nil {
			return nil, err
		}
		return &NatPmpSource{Gateway: gw, Lifetime: sec.Int("lifetime", 120),
			Timeout: sec.Float("timeout", 3), Retries: sec.Int("retries", 3)}, nil
	})
}
