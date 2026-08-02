package source

import (
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sixxhands/fyrwire/internal/config"
	"github.com/sixxhands/fyrwire/internal/model"
)

type IPortSource interface {
	Name() string
	Resolve(reqs []model.PortRequestData) (model.EndpointData, error)
}

var Logf func(string)

var registry = map[string]func(config.Section) (IPortSource, error){}

func register(name string, opts []config.OptionRule,
	build func(config.Section) (IPortSource, error)) {
	registry[name] = build
	config.RegisterSource(name, opts)
}

func Build(sec config.Section) (IPortSource, error) {
	build, ok := registry[sec.Type()]
	if !ok {
		return nil, model.Errorf("unknown source type %q", sec.Type())
	}
	return build(sec)
}

func Available() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}

type StaticSource struct {
	Host  string
	Ports map[string]int
}

func (s *StaticSource) Name() string { return "static" }

func (s *StaticSource) Resolve(reqs []model.PortRequestData) (model.EndpointData, error) {
	res := model.EndpointData{Host: s.Host, Ports: map[string]int{},
		Partial: map[string]bool{}}
	for _, r := range reqs {
		if p, ok := s.Ports[r.Key]; ok {
			res.Ports[r.Key] = p
		} else {
			res.Ports[r.Key] = r.InternalPort
		}
	}
	return res, nil
}

type CommandSource struct {
	Command     string
	Host        string
	HostCommand string
	Timeout     int
}

func (c *CommandSource) Name() string { return "command" }

func (c *CommandSource) Resolve(reqs []model.PortRequestData) (model.EndpointData, error) {
	out, err := run(c.Command, c.Timeout)
	if err != nil {
		return model.EndpointData{}, model.Errorf("source command failed: %v", err)
	}
	host, port, err := parseEndpoint(strings.TrimSpace(out))
	if err != nil {
		return model.EndpointData{}, err
	}
	if host == "" {
		host = c.Host
		if c.HostCommand != "" {
			h, err := run(c.HostCommand, c.Timeout)
			if err != nil {
				return model.EndpointData{}, model.Errorf("host_command failed: %v", err)
			}
			host = strings.TrimSpace(h)
		}
	}
	if host == "" {
		return model.EndpointData{}, model.Errorf(
			"the source command printed a port but no address, and neither " +
				"host nor host_command is set")
	}
	res := model.EndpointData{Host: host, Ports: map[string]int{},
		Partial: map[string]bool{}}

	for _, r := range reqs {
		res.Ports[r.Key] = port
	}
	return res, nil
}

func parseEndpoint(s string) (string, int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, model.Errorf("the source command printed nothing")
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		port, err := strconv.Atoi(strings.TrimSpace(s[i+1:]))
		if err != nil {
			return "", 0, model.Errorf("cannot read a port from %q", s)
		}
		return strings.TrimSpace(s[:i]), port, nil
	}
	port, err := strconv.Atoi(s)
	if err != nil {
		return "", 0, model.Errorf("cannot read a port from %q", s)
	}
	return "", port, nil
}

func run(cmdline string, timeout int) (string, error) {
	if timeout <= 0 {
		timeout = 15
	}
	sh, flag := "sh", "-c"
	out, err := exec.Command(sh, flag, cmdline).Output()
	_ = timeout
	_ = time.Second
	return string(out), err
}

func init() {
	register("static", []config.OptionRule{
		{Name: "host", Required: true, Kind: "string"},
		{Name: "ports", Kind: "table"},
	}, func(sec config.Section) (IPortSource, error) {
		host, err := sec.String("host")
		if err != nil {
			return nil, err
		}
		ports := map[string]int{}
		if raw, ok := sec["ports"].(map[string]any); ok {
			for k, v := range raw {
				if n, ok := v.(int64); ok {
					ports[k] = int(n)
				}
			}
		}
		return &StaticSource{Host: host, Ports: ports}, nil
	})

	register("command", []config.OptionRule{
		{Name: "command", Required: true, Kind: "string"},
		{Name: "host", Kind: "string"},
		{Name: "host_command", Kind: "string"},
		{Name: "timeout", Kind: "int"},
	}, func(sec config.Section) (IPortSource, error) {
		cmd, err := sec.String("command")
		if err != nil {
			return nil, err
		}
		host, _ := sec.String("host")
		hostCmd, _ := sec.String("host_command")
		return &CommandSource{Command: cmd, Host: host, HostCommand: hostCmd,
			Timeout: sec.Int("timeout", 15)}, nil
	})
}
