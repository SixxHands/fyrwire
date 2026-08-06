package config

import (
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

const SchemaVersion = 1

type MetaData struct {
	Version *int `toml:"version"`
}

type UdpConfigData struct {
	IdleNew         *int     `toml:"idle_new"`
	IdleEstablished *int     `toml:"idle_established"`
	MaxSessions     *int     `toml:"max_sessions"`
	MaxPerIp        *int     `toml:"max_per_ip"`
	NewPerSec       *float64 `toml:"new_per_sec"`
	ReapInterval    *float64 `toml:"reap_interval"`
	DropZeroLength  *bool    `toml:"drop_zero_length"`
}

type NodeConfigData struct {
	AName      string        `toml:"a_name"`
	Ttl        *int          `toml:"ttl"`
	Relay      bool          `toml:"relay"`
	RelayUdp   bool          `toml:"relay_udp"`
	BindHost   string        `toml:"bind_host"`
	TargetHost string        `toml:"target_host"`
	Udp        UdpConfigData `toml:"udp"`
}

type ServiceConfigData struct {
	Name         string `toml:"name"`
	InternalPort *int   `toml:"internal_port"`
	SrvName      string `toml:"srv_name"`
	Protocol     string `toml:"protocol"`
	Preset       string `toml:"preset"`
	Priority     int    `toml:"priority"`
	Weight       *int   `toml:"weight"`
	Ttl          *int   `toml:"ttl"`
	Record       string `toml:"record"`
}

type AgentConfigData struct {
	Interval int `toml:"interval"`
	Jitter   int `toml:"jitter"`
}

type AlertsConfigData struct {
	Webhook       string `toml:"webhook"`
	Command       string `toml:"command"`
	AfterFailures int    `toml:"after_failures"`
}

type ConfigData struct {
	FyrWire  MetaData            `toml:"fyrwire"`
	Source   Section             `toml:"source"`
	Provider Section             `toml:"provider"`
	Node     NodeConfigData      `toml:"node"`
	Service  []ServiceConfigData `toml:"service"`
	Agent    AgentConfigData     `toml:"agent"`
	Alerts   AlertsConfigData    `toml:"alerts"`
}

type Section map[string]any

func (s Section) Type() string {
	if v, ok := s["type"].(string); ok {
		return v
	}
	return ""
}

func (s Section) String(key string) (string, error) {
	v, ok := s[key]
	if !ok {
		return "", nil
	}
	str, ok := v.(string)
	if !ok {
		return "", model.Errorf("%s must be a string", key)
	}
	if name, found := strings.CutPrefix(str, "env:"); found {
		got := os.Getenv(name)
		if got == "" {
			return "", model.Errorf("%s references $%s, which is unset", key, name)
		}
		return got, nil
	}
	return str, nil
}

func (s Section) Int(key string, fallback int) int {
	switch v := s[key].(type) {
	case int64:
		return int(v)
	case int:
		return v
	}
	return fallback
}

func (s Section) Float(key string, fallback float64) float64 {
	switch v := s[key].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	}
	return fallback
}

type OptionRule struct {
	Name     string
	Required bool
	Kind     string
}

var (
	SourceOptions   = map[string][]OptionRule{}
	ProviderOptions = map[string][]OptionRule{}
)

func RegisterSource(name string, options []OptionRule)   { SourceOptions[name] = options }
func RegisterProvider(name string, options []OptionRule) { ProviderOptions[name] = options }

var PresetLookup = func(key string) (record, protocol string, ok bool) {
	return "", "", true
}

func Load(path string) (*ConfigData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, model.Errorf(
				"no config at %s (run `fyrwire init` to write a starter)", path)
		}
		return nil, model.Errorf("cannot read %s: %v", path, err)
	}
	return ReadText(string(data))
}

func ReadText(text string) (*ConfigData, error) {
	var raw map[string]any
	if _, err := toml.Decode(text, &raw); err != nil {
		return nil, model.Errorf("not valid TOML: %v", err)
	}
	if err := checkStructure(raw); err != nil {
		return nil, err
	}
	var loaded ConfigData
	if _, err := toml.Decode(text, &loaded); err != nil {
		return nil, model.Errorf("not valid TOML: %v", err)
	}
	if err := checkConfig(&loaded); err != nil {
		return nil, err
	}
	return &loaded, nil
}

func (c *ConfigData) Plan() (model.NodePlanData, error) {
	ttl := 60
	if c.Node.Ttl != nil {
		ttl = *c.Node.Ttl
	}
	out := make([]model.ServiceData, 0, len(c.Service))
	for _, s := range c.Service {
		svc, err := buildServicePlan(s)
		if err != nil {
			return model.NodePlanData{}, err
		}
		out = append(out, svc)
	}
	return model.NodePlanData{AName: c.Node.AName, Services: out, Ttl: ttl}, nil
}

func buildServicePlan(s ServiceConfigData) (model.ServiceData, error) {
	record := s.Record
	proto := s.Protocol
	if s.Preset != "" {
		presetRecord, presetProto, ok := PresetLookup(s.Preset)
		if !ok {
			return model.ServiceData{}, model.Errorf("unknown preset %q", s.Preset)
		}
		if proto == "" && presetProto != "" {
			proto = presetProto
		}
		if record == "" {
			record = presetRecord
		}
	}
	if record == "" {
		record = model.RecordSrv
	}
	if proto == "" {
		proto = "tcp"
	}
	weight := 5
	if s.Weight != nil {
		weight = *s.Weight
	}
	serviceTtl := 0
	if s.Ttl != nil {
		serviceTtl = *s.Ttl
	}
	return model.ServiceData{
		Key: s.Name, InternalPort: *s.InternalPort, SrvName: s.SrvName,
		Protocol: proto, Priority: s.Priority, Weight: weight,
		Ttl: serviceTtl, Record: record,
	}, nil
}
