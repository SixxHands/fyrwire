package config

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/sixxhands/fyrwire/internal/model"
)

const SchemaVersion = 1

type Meta struct {
	Version *int `toml:"version"`
}

type UDP struct {
	IdleNew         *int     `toml:"idle_new"`
	IdleEstablished *int     `toml:"idle_established"`
	MaxSessions     *int     `toml:"max_sessions"`
	MaxPerIP        *int     `toml:"max_per_ip"`
	NewPerSec       *float64 `toml:"new_per_sec"`
	ReapInterval    *float64 `toml:"reap_interval"`
	DropZeroLength  *bool    `toml:"drop_zero_length"`
}

type Node struct {
	AName      string `toml:"a_name"`
	TTL        *int   `toml:"ttl"`
	Relay      bool   `toml:"relay"`
	RelayUDP   bool   `toml:"relay_udp"`
	BindHost   string `toml:"bind_host"`
	TargetHost string `toml:"target_host"`
	UDP        UDP    `toml:"udp"`
}

type Service struct {
	Name         string `toml:"name"`
	InternalPort *int   `toml:"internal_port"`
	SrvName      string `toml:"srv_name"`
	Protocol     string `toml:"protocol"`
	Preset       string `toml:"preset"`
	Priority     int    `toml:"priority"`
	Weight       *int   `toml:"weight"`
	TTL          *int   `toml:"ttl"`
	Record       string `toml:"record"`
}

type Agent struct {
	Interval int `toml:"interval"`
	Jitter   int `toml:"jitter"`
}

type Alerts struct {
	Webhook       string `toml:"webhook"`
	Command       string `toml:"command"`
	AfterFailures int    `toml:"after_failures"`
}

type Config struct {
	FyrWire  Meta      `toml:"fyrwire"`
	Source   Section   `toml:"source"`
	Provider Section   `toml:"provider"`
	Node     Node      `toml:"node"`
	Service  []Service `toml:"service"`
	Agent    Agent     `toml:"agent"`
	Alerts   Alerts    `toml:"alerts"`
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

func (s Section) Int(key string, def int) int {
	switch v := s[key].(type) {
	case int64:
		return int(v)
	case int:
		return v
	}
	return def
}

func (s Section) Float(key string, def float64) float64 {
	switch v := s[key].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	}
	return def
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

func RegisterSource(name string, opts []OptionRule)   { SourceOptions[name] = opts }
func RegisterProvider(name string, opts []OptionRule) { ProviderOptions[name] = opts }

var enums = map[string][]string{
	"service.protocol": {"tcp", "udp"},
	"service.record":   {"srv", "https", "none"},
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, model.Errorf(
				"no config at %s — run `fyrwire init` to write a starter", path)
		}
		return nil, model.Errorf("cannot read %s: %v", path, err)
	}
	return Parse(string(data))
}

func Parse(text string) (*Config, error) {

	var raw map[string]any
	if _, err := toml.Decode(text, &raw); err != nil {
		return nil, model.Errorf("not valid TOML: %v", err)
	}
	if err := checkStructure(raw); err != nil {
		return nil, err
	}

	var cfg Config
	if _, err := toml.Decode(text, &cfg); err != nil {

		return nil, model.Errorf("not valid TOML: %v", err)
	}
	if err := checkVersion(cfg.FyrWire); err != nil {
		return nil, err
	}
	if err := checkRequiredSections(&cfg); err != nil {
		return nil, err
	}
	if err := checkSection("source", cfg.Source, SourceOptions, "source"); err != nil {
		return nil, err
	}
	if err := checkSection("provider", cfg.Provider, ProviderOptions, "provider"); err != nil {
		return nil, err
	}
	if err := checkServices(cfg.Service); err != nil {
		return nil, err
	}
	if err := checkCross(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

var knownSections = []string{"fyrwire", "source", "provider", "node", "service",
	"agent", "alerts"}

func checkStructure(raw map[string]any) error {
	top := schemaOf(reflect.TypeOf(Config{}))
	for key, value := range raw {
		spec, ok := top[key]
		if !ok {
			return model.Errorf("unknown section [%s]%s", key,
				suggest(key, knownSections))
		}
		switch key {
		case "source", "provider":

			if _, isTable := value.(map[string]any); !isTable {
				return model.Errorf(
					"[%s] must be a table; got %s. Write it as [%s] with keys "+
						"underneath, not %s = ...", key, humanKind(tomlKind(value)),
					key, key)
			}
			continue
		case "service":

			list, err := asTableList(value)
			if err != nil {
				return err
			}
			for i, svc := range list {
				if err := checkTable(fmtIndex(key, i), svc, spec.sub); err != nil {
					return err
				}
			}
			continue
		}
		table, ok := value.(map[string]any)
		if !ok {
			return model.Errorf(
				"[%s] must be a table; got %s. Write it as [%s] with keys "+
					"underneath, not %s = ...", key, humanKind(tomlKind(value)),
				key, key)
		}
		if err := checkTable(key, table, spec.sub); err != nil {
			return err
		}
	}
	return nil
}

func asTableList(v any) ([]map[string]any, error) {
	switch t := v.(type) {
	case []map[string]any:
		return t, nil
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, item := range t {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, model.Errorf(
					"services must be written as [[service]] (double brackets), " +
						"one table per service")
			}
			out = append(out, m)
		}
		return out, nil
	}
	return nil, model.Errorf(
		"services must be written as [[service]] (double brackets), " +
			"one table per service")
}

func fmtIndex(name string, i int) string {
	return name + "[" + strconv.Itoa(i) + "]"
}

func checkVersion(m Meta) error {
	v := SchemaVersion
	if m.Version != nil {
		v = *m.Version
	}
	if v > SchemaVersion {
		return model.Errorf(
			"this config is version %d, but this FyrWire understands up to %d. "+
				"Upgrade FyrWire, or remove the newer keys — guessing what they "+
				"mean would be worse than stopping.", v, SchemaVersion)
	}
	if v < 1 {
		return model.Errorf("config version must be 1 or greater, got %d", v)
	}
	return nil
}

func checkRequiredSections(cfg *Config) error {
	if len(cfg.Source) == 0 {
		return model.Errorf("config is missing the [source] section")
	}
	if len(cfg.Provider) == 0 {
		return model.Errorf("config is missing the [provider] section")
	}
	if cfg.Node.AName == "" {
		return model.Errorf("node is missing the required key \"a_name\"")
	}
	if len(cfg.Service) == 0 {
		return model.Errorf("config defines no [[service]] entries")
	}
	return nil
}

func checkSection(where string, sec Section, registry map[string][]OptionRule,
	kind string) error {
	rawType, present := sec["type"]
	if !present {
		return model.Errorf("[%s] needs a `type` key; available: %s",
			where, strings.Join(sortedKeys(registry), ", "))
	}
	t, isString := rawType.(string)
	if !isString {

		return model.Errorf(
			"%s.type must be a string naming a %s; available: %s",
			where, kind, strings.Join(sortedKeys(registry), ", "))
	}
	opts, ok := registry[t]
	if !ok {
		return model.Errorf("unknown %s type %q%s", where, t,
			suggest(t, sortedKeys(registry)))
	}
	valid := map[string]OptionRule{}
	var names []string
	for _, o := range opts {
		valid[o.Name] = o
		names = append(names, o.Name)
	}
	for key, value := range sec {
		if key == "type" {
			continue
		}
		spec, ok := valid[key]
		if !ok {
			return model.Errorf("unknown key %s.%s for %s type %q%s",
				where, key, where, t, suggest(key, names))
		}

		if s, isStr := value.(string); isStr && strings.HasPrefix(s, "env:") {
			continue
		}
		if err := checkKind(where+"."+key, value, spec.Kind); err != nil {
			return err
		}
	}
	for _, o := range opts {
		if o.Required {
			if _, present := sec[o.Name]; !present {
				return model.Errorf("[%s] type %q requires %q", where, t, o.Name)
			}
		}
	}
	return nil
}

func checkKind(where string, v any, kind string) error {
	switch kind {
	case "string":
		if _, ok := v.(string); !ok {
			return model.Errorf("%s must be a string, got %T", where, v)
		}
	case "int":
		if _, ok := v.(int64); !ok {
			return model.Errorf("%s must be an integer, got %T", where, v)
		}
	case "float":
		switch v.(type) {
		case float64, int64:
		default:
			return model.Errorf("%s must be a number, got %T", where, v)
		}
	case "bool":
		if _, ok := v.(bool); !ok {
			return model.Errorf("%s must be true or false, got %T", where, v)
		}
	case "table":
		if _, ok := v.(map[string]any); !ok {
			return model.Errorf("%s must be a table, got %T", where, v)
		}
	}
	return nil
}

func checkServices(svcs []Service) error {
	seen := map[string]bool{}
	for i, s := range svcs {
		if s.Name == "" {
			return model.Errorf("service[%d] is missing the required key \"name\"", i)
		}
		if seen[s.Name] {
			return model.Errorf("duplicate service name %q", s.Name)
		}
		seen[s.Name] = true
		if s.InternalPort == nil {
			return model.Errorf(
				"service[%d] is missing the required key \"internal_port\"", i)
		}
		if *s.InternalPort < 1 || *s.InternalPort > 65535 {

			return model.Errorf(
				"service[%d].internal_port is %d — a port is 1..65535",
				i, *s.InternalPort)
		}
		if s.SrvName == "" {
			return model.Errorf("service[%d] is missing the required key \"srv_name\"", i)
		}
		if s.Protocol != "" && !contains(enums["service.protocol"], s.Protocol) {
			return model.Errorf("service[%d].protocol must be one of %s, got %q",
				i, strings.Join(enums["service.protocol"], ", "), s.Protocol)
		}
		if s.Record != "" && !contains(enums["service.record"], s.Record) {
			return model.Errorf("service[%d].record must be one of %s, got %q",
				i, strings.Join(enums["service.record"], ", "), s.Record)
		}
	}
	return nil
}

func checkCross(cfg *Config) error {

	if cfg.Node.RelayUDP && !cfg.Node.Relay {
		return model.Errorf(
			"node.relay_udp is set but node.relay is not — UDP relaying is part " +
				"of the relay, so this would silently do nothing")
	}
	return nil
}

func (cfg *Config) Plan() (model.NodePlanData, error) {
	ttl := 60
	if cfg.Node.TTL != nil {
		ttl = *cfg.Node.TTL
	}
	out := make([]model.Service, 0, len(cfg.Service))
	for _, s := range cfg.Service {
		var protoOverride *string
		record := s.Record
		if s.Preset != "" {
			p, proto, ok := PresetLookup(s.Preset)
			if !ok {
				return model.NodePlanData{}, model.Errorf("unknown preset %q", s.Preset)
			}

			if s.Protocol == "" && proto != "" {
				proto2 := proto
				protoOverride = &proto2
			}

			if record == "" {
				record = p
			}
		}
		if record == "" {
			record = model.RecordSRV
		}
		proto := s.Protocol
		if proto == "" && protoOverride != nil {
			proto = *protoOverride
		}
		if proto == "" {
			proto = "tcp"
		}
		weight := 5
		if s.Weight != nil {
			weight = *s.Weight
		}
		svcTTL := 0
		if s.TTL != nil {
			svcTTL = *s.TTL
		}
		out = append(out, model.Service{
			Key: s.Name, InternalPort: *s.InternalPort, SrvName: s.SrvName,
			Protocol: proto, Priority: s.Priority, Weight: weight,
			TTL: svcTTL, Record: record,
		})
	}
	return model.NodePlanData{AName: cfg.Node.AName, Services: out, TTL: ttl}, nil
}

var PresetLookup = func(key string) (record, protocol string, ok bool) {
	return "", "", true
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Typo catcher: if you write 'relayy' or 'zoneid', complain loudly instead of silently ignoring it.
func suggest(name string, known []string) string {
	best, bestD := "", 1<<30
	for _, k := range known {
		if d := editDistance(name, k); d < bestD {
			best, bestD = k, d
		}
	}
	limit := len(name) / 2
	if limit < 2 {
		limit = 2
	}
	if best != "" && bestD <= limit {
		return fmt.Sprintf(" — did you mean %q?", best)
	}
	sorted := append([]string(nil), known...)
	sort.Strings(sorted)
	if len(sorted) == 0 {
		return ""
	}
	return " — valid keys: " + strings.Join(sorted, ", ")
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(min(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
