package source

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/sixxhands/fyrwire/internal/config"
	"github.com/sixxhands/fyrwire/internal/model"
)

type UpnpSource struct {
	Binary  string
	Timeout int
}

func (u *UpnpSource) Name() string { return "upnp" }

var (
	upnpExternalIP = regexp.MustCompile(`ExternalIPAddress\s*=\s*([0-9.]+)`)
	upnpMapped     = regexp.MustCompile(`(?i)external\s+[0-9.]+:(\d+)`)
)

func (u *UpnpSource) run(args ...string) (string, error) {
	bin := u.Binary
	if bin == "" {
		bin = "upnpc"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return "", model.Errorf(
			"%s is not installed. The upnp source needs miniupnpc — install it, "+
				"or use the natpmp source if your router speaks NAT-PMP.", bin)
	}
	t := u.Timeout
	if t <= 0 {
		t = 30
	}
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(t)*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if ctx.Err() != nil {
		return string(out), model.Errorf("%s timed out after %ds", bin, t)
	}
	return string(out), err
}

func (u *UpnpSource) Resolve(reqs []model.PortRequestData) (model.EndpointData, error) {
	out, err := u.run("-s")
	if err != nil && out == "" {
		return model.EndpointData{}, err
	}
	m := upnpExternalIP.FindStringSubmatch(out)
	if m == nil {
		return model.EndpointData{}, model.Errorf(
			"could not read an external address from upnpc — the router may not " +
				"have IGD enabled")
	}
	res := model.EndpointData{Host: m[1], Ports: map[string]int{},
		Partial: map[string]bool{}}
	for _, r := range reqs {
		proto := "TCP"
		if r.Protocol == "udp" {
			proto = "UDP"
		}
		port := strconv.Itoa(r.InternalPort)
		mapped, err := u.run("-a", "", port, port, proto)
		if err != nil {

			res.Partial[r.Key] = true
			continue
		}
		granted := r.InternalPort
		if mm := upnpMapped.FindStringSubmatch(mapped); mm != nil {
			if n, convErr := strconv.Atoi(mm[1]); convErr == nil && n > 0 {

				granted = n
			}
		}
		res.Ports[r.Key] = granted
	}
	if len(res.Ports) == 0 && len(reqs) > 0 {
		return model.EndpointData{}, model.Errorf(
			"the router granted no ports at all — check that UPnP is enabled")
	}
	return res, nil
}

func init() {
	register("upnp", []config.OptionRule{
		{Name: "binary", Kind: "string"},
		{Name: "timeout", Kind: "int"},
	}, func(sec config.Section) (IPortSource, error) {
		bin, _ := sec.String("binary")
		return &UpnpSource{Binary: bin, Timeout: sec.Int("timeout", 30)}, nil
	})
}
