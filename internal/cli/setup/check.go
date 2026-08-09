package setup

import (
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/sixxhands/fyrwire/internal/node/config"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

func Check(configPath string, timeout time.Duration) error {
	loaded, err := config.Load(configPath)
	if err != nil {
		return err
	}
	plan, err := loaded.Plan()
	if err != nil {
		return err
	}
	allOk := true
	for _, svc := range plan.Services {
		if !checkService(svc, timeout) {
			allOk = false
		}
	}
	if !allOk {
		return model.Errorf("one or more checks failed")
	}
	return nil
}

func checkService(svc model.ServiceData, timeout time.Duration) bool {
	say("  %s%s%s", text, svc.Key, reset)
	if svc.Record != "srv" {
		say("    %spublished as %s; skipping SRV check%s", dim, svc.Record, reset)
		return true
	}
	parts := strings.SplitN(strings.TrimPrefix(svc.SrvName, "_"), ".", 3)
	if len(parts) != 3 {
		fail("%s does not look like an SRV name", svc.SrvName)
		return false
	}
	_, addrs, err := net.LookupSRV(parts[0], strings.TrimPrefix(parts[1], "_"), parts[2])
	if err != nil || len(addrs) == 0 {
		fail("SRV %s lookup failed (%v)", svc.SrvName, err)
		return false
	}
	passed := true
	for _, a := range addrs {
		if !checkSrvTarget(svc.Protocol, a, timeout) {
			passed = false
		}
	}
	return passed
}

func checkSrvTarget(protocol string, a *net.SRV, timeout time.Duration) bool {
	host := strings.TrimSuffix(a.Target, ".")
	good("SRV -> %s:%d", host, a.Port)
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		fail("  %s has no A record", host)
		return false
	}
	passed := true
	for _, ip := range ips {
		if protocol != "tcp" {
			// Cannot run a real connect check on UDP without speaking the game protocol, so just check that A exists.
			say("    %sA -> %s (UDP, cannot test with a TCP connect)%s", dim, ip, reset)
			continue
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(int(a.Port))), timeout)
		if err != nil {
			fail("  TCP %s:%d unreachable (%v)", ip, a.Port, err)
			passed = false
			continue
		}
		conn.Close()
		good("  TCP %s:%d accepts connections", ip, a.Port)
	}
	return passed
}
