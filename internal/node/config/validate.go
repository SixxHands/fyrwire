package config

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/sixxhands/fyrwire/internal/node/model"
)

var (
	knownSections = []string{"fyrwire", "source", "provider", "node", "service", "agent", "alerts"}
	enums         = map[string][]string{
		"service.protocol": {"tcp", "udp"},
		"service.record":   {"srv", "https", "none"},
	}
	kindCheckers = map[string]func(where string, v any) error{
		"string": func(where string, v any) error {
			if _, ok := v.(string); !ok {
				return model.Errorf("%s must be a string, got %T", where, v)
			}
			return nil
		},
		"int": func(where string, v any) error {
			if _, ok := v.(int64); !ok {
				return model.Errorf("%s must be an integer, got %T", where, v)
			}
			return nil
		},
		"float": func(where string, v any) error {
			switch v.(type) {
			case float64, int64:
				return nil
			default:
				return model.Errorf("%s must be a number, got %T", where, v)
			}
		},
		"bool": func(where string, v any) error {
			if _, ok := v.(bool); !ok {
				return model.Errorf("%s must be true or false, got %T", where, v)
			}
			return nil
		},
		"table": func(where string, v any) error {
			if _, ok := v.(map[string]any); !ok {
				return model.Errorf("%s must be a table, got %T", where, v)
			}
			return nil
		},
	}
)

func checkConfig(loaded *ConfigData) error {
	if err := checkVersion(loaded.FyrWire); err != nil {
		return err
	}
	if err := checkRequiredSections(loaded); err != nil {
		return err
	}
	if err := checkSection("source", loaded.Source, SourceOptions, "source"); err != nil {
		return err
	}
	if err := checkSection("provider", loaded.Provider, ProviderOptions, "provider"); err != nil {
		return err
	}
	if err := checkServices(loaded.Service); err != nil {
		return err
	}
	return checkCross(loaded)
}

func checkStructure(raw map[string]any) error {
	top := schemaOf(reflect.TypeOf(ConfigData{}))
	for key, value := range raw {
		rule, ok := top[key]
		if !ok {
			return model.Errorf("unknown section [%s]%s", key, suggest(key, knownSections))
		}
		if err := checkTopSection(key, value, rule); err != nil {
			return err
		}
	}
	return nil
}

func checkTopSection(key string, value any, rule fieldRule) error {
	switch key {
	case "source", "provider":
		if _, isTable := value.(map[string]any); !isTable {
			return tableError(key, value)
		}
		return nil
	case "service":
		list, err := asTableList(value)
		if err != nil {
			return err
		}
		for i, svc := range list {
			if err := checkTable(key+"["+strconv.Itoa(i)+"]", svc, rule.sub); err != nil {
				return err
			}
		}
		return nil
	}
	table, ok := value.(map[string]any)
	if !ok {
		return tableError(key, value)
	}
	return checkTable(key, table, rule.sub)
}

func tableError(key string, value any) error {
	return model.Errorf(
		"[%s] must be a table; got %s. Write it as [%s] with keys underneath, not %s = ...",
		key, humanKind(tomlKind(value)), key, key)
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
					"services must be written as [[service]] (double brackets), one table per service")
			}
			out = append(out, m)
		}
		return out, nil
	}
	return nil, model.Errorf(
		"services must be written as [[service]] (double brackets), one table per service")
}

func checkVersion(m MetaData) error {
	v := SchemaVersion
	if m.Version != nil {
		v = *m.Version
	}
	if v > SchemaVersion {
		return model.Errorf(
			"config version %d is newer than supported version %d; upgrade fyrwire or remove newer keys",
			v, SchemaVersion)
	}
	if v < 1 {
		return model.Errorf("config version must be 1 or greater, got %d", v)
	}
	return nil
}

func checkRequiredSections(loaded *ConfigData) error {
	if len(loaded.Source) == 0 {
		return model.Errorf("config is missing the [source] section")
	}
	if len(loaded.Provider) == 0 {
		return model.Errorf("config is missing the [provider] section")
	}
	if loaded.Node.AName == "" {
		return model.Errorf("node is missing the required key \"a_name\"")
	}
	if len(loaded.Service) == 0 {
		return model.Errorf("config defines no [[service]] entries")
	}
	return nil
}

func checkSection(where string, sec Section, registry map[string][]OptionRule, kind string) error {
	rawType, present := sec["type"]
	if !present {
		return model.Errorf("[%s] needs a `type` key; available: %s",
			where, strings.Join(sortedKeys(registry), ", "))
	}
	t, isString := rawType.(string)
	if !isString {
		return model.Errorf("%s.type must be a string naming a %s; available: %s",
			where, kind, strings.Join(sortedKeys(registry), ", "))
	}
	options, ok := registry[t]
	if !ok {
		return model.Errorf("unknown %s type %q%s", where, t, suggest(t, sortedKeys(registry)))
	}
	return checkSectionOptions(where, t, sec, options)
}

func checkSectionOptions(where, typeName string, sec Section, options []OptionRule) error {
	valid := map[string]OptionRule{}
	names := make([]string, 0, len(options))
	for _, o := range options {
		valid[o.Name] = o
		names = append(names, o.Name)
	}
	for key, value := range sec {
		if key == "type" {
			continue
		}
		rule, ok := valid[key]
		if !ok {
			return model.Errorf("unknown key %s.%s for %s type %q%s",
				where, key, where, typeName, suggest(key, names))
		}
		if s, isStr := value.(string); isStr && strings.HasPrefix(s, "env:") {
			continue
		}
		if err := checkKind(where+"."+key, value, rule.Kind); err != nil {
			return err
		}
	}
	for _, o := range options {
		if o.Required {
			if _, present := sec[o.Name]; !present {
				return model.Errorf("[%s] type %q requires %q", where, typeName, o.Name)
			}
		}
	}
	return nil
}

func checkKind(where string, v any, kind string) error {
	if check, ok := kindCheckers[kind]; ok {
		return check(where, v)
	}
	return nil
}

func checkServices(services []ServiceConfigData) error {
	seen := map[string]bool{}
	for i, s := range services {
		if s.Name == "" {
			return model.Errorf("service[%d] is missing the required key \"name\"", i)
		}
		if seen[s.Name] {
			return model.Errorf("duplicate service name %q", s.Name)
		}
		seen[s.Name] = true
		if s.InternalPort == nil {
			return model.Errorf("service[%d] is missing the required key \"internal_port\"", i)
		}
		if *s.InternalPort < 1 || *s.InternalPort > 65535 {
			return model.Errorf("service[%d].internal_port is %d (must be 1..65535)", i, *s.InternalPort)
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

func checkCross(loaded *ConfigData) error {
	if loaded.Node.RelayUdp && !loaded.Node.Relay {
		return model.Errorf("node.relay_udp is set but node.relay is false; enable node.relay too")
	}
	return nil
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

// Typo catcher: if you write 'relayy' or 'zoneid', complain instead of ignoring it.
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
		return fmt.Sprintf(" (did you mean %q?)", best)
	}
	sorted := append([]string(nil), known...)
	sort.Strings(sorted)
	if len(sorted) == 0 {
		return ""
	}
	return " (valid keys: " + strings.Join(sorted, ", ") + ")"
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
