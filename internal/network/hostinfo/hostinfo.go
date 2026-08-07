package hostinfo

import (
	"net"
	"strings"
	"time"
)

const routeCheckHost = "1.1.1.1"

type InterfaceData struct {
	Name      string
	Address   string
	PrefixLen int
}

type PortData struct {
	Port    int
	Process string
}

func (item InterfaceData) IsTunnel() bool {
	for _, prefix := range []string{"wg", "tun", "utun", "proton", "tap", "ppp"} {
		if strings.HasPrefix(item.Name, prefix) {
			return true
		}
	}

	return item.PrefixLen == 32
}

func LocalAddressToward(host string) string {
	conn, err := net.DialTimeout("udp4", net.JoinHostPort(host, "9"), 2*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()

	address, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}

	return address.IP.String()
}

func EgressAddress() string {
	return LocalAddressToward(routeCheckHost)
}

func SubnetRouter(address string) string {
	parts := strings.Split(address, ".")
	if len(parts) != 4 {
		return ""
	}

	return strings.Join(append(parts[:3:3], "1"), ".")
}

func Interfaces() []InterfaceData {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var results []InterfaceData
	for _, item := range interfaces {
		if item.Flags&net.FlagLoopback != 0 {
			continue
		}

		results = appendIpv4Addresses(results, item)
	}

	return results
}

func appendIpv4Addresses(results []InterfaceData, item net.Interface) []InterfaceData {
	addresses, err := item.Addrs()
	if err != nil {
		return results
	}

	for _, address := range addresses {
		network, ok := address.(*net.IPNet)
		if !ok || network.IP.To4() == nil {
			continue
		}

		ones, _ := network.Mask.Size()
		results = append(results, InterfaceData{
			Name:      item.Name,
			Address:   network.IP.String(),
			PrefixLen: ones,
		})
	}

	return results
}
