package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sixxhands/fyrwire/internal/config"
	"github.com/sixxhands/fyrwire/internal/model"
)

type RateLimited struct {
	Msg        string
	RetryAfter time.Duration
}

func (e *RateLimited) Error() string { return e.Msg }

type IDnsProvider interface {
	Name() string

	EnsureA(name, ip string, ttl int) (string, error)
	EnsureSRV(rec model.SrvRecordData) (string, error)
	EnsureHTTPS(rec model.HttpsRecordData) (string, error)

	Delete(rtype, name, expect string) (string, error)
}

var registry = map[string]func(config.Section) (IDnsProvider, error){}

func register(name string, opts []config.OptionRule,
	build func(config.Section) (IDnsProvider, error)) {
	registry[name] = build
	config.RegisterProvider(name, opts)
}

func Build(sec config.Section) (IDnsProvider, error) {
	build, ok := registry[sec.Type()]
	if !ok {
		return nil, model.Errorf("unknown provider type %q", sec.Type())
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

func TargetOf(rtype, value string) string {
	f := strings.Fields(value)
	switch rtype {
	case "SRV":
		if len(f) >= 4 {
			return f[3]
		}
	case "HTTPS", "SVCB":
		if len(f) >= 2 {
			return f[1]
		}
	case "A", "AAAA", "CNAME":
		if len(f) >= 1 {
			return f[0]
		}
	}
	return ""
}

func sameHost(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}

func CheckTarget(target string) error {
	t := strings.TrimSuffix(strings.TrimSpace(target), ".")
	if t == "" {
		return model.Errorf("SRV/HTTPS target is empty")
	}
	if isIPv4Literal(t) {
		return model.Errorf(
			"%q is an IP address, but an SRV/HTTPS target must be a hostname. "+
				"Point node.a_name at a NAME whose A record holds the address — "+
				"an IP here produces a record that resolves fine and then fails "+
				"in the client.", target)
	}
	if strings.Contains(t, "_") {
		return model.Errorf(
			"%q contains an underscore. That is legal in the SRV NAME "+
				"(_minecraft._tcp....) but not in its TARGET, and the API "+
				"rejects it with a parse error that names neither.", target)
	}
	return nil
}

func isIPv4Literal(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 {
			return false
		}
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

func Canon(rtype, value string) string {
	f := strings.Fields(value)
	switch rtype {
	case "HTTPS", "SVCB":
		if len(f) >= 2 {
			params := make([]string, 0, len(f))
			for _, p := range f[2:] {
				params = append(params, strings.ReplaceAll(p, `"`, ""))
			}
			sortStrings(params)
			return strings.Join(append([]string{f[0],
				strings.ToLower(strings.TrimSuffix(f[1], "."))}, params...), " ")
		}
	case "SRV":
		if len(f) >= 4 {
			return strings.Join([]string{f[0], f[1], f[2],
				strings.ToLower(strings.TrimSuffix(f[3], "."))}, " ")
		}
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func httpDo(client *http.Client, method, url string, body []byte,
	headers map[string]string) (int, []byte, http.Header, error) {
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return 0, nil, nil, model.Errorf("bad request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, model.Errorf("unreachable: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data, resp.Header, nil
}

func retryAfter(h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return 0
}

func jsonOr(data []byte, fallback string) string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		s := strings.TrimSpace(string(data))
		if s == "" {
			return fallback
		}
		return truncate(s, 200)
	}
	return truncate(flatten(v, ""), 300)
}

func flatten(v any, prefix string) string {
	switch t := v.(type) {
	case string:
		if prefix != "" {
			return prefix + ": " + t
		}
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, i := range t {
			parts = append(parts, flatten(i, prefix))
		}
		return strings.Join(parts, "; ")
	case map[string]any:
		parts := make([]string, 0, len(t))
		for k, i := range t {
			p := k
			if k == "detail" || k == "message" {
				p = prefix
			}
			parts = append(parts, flatten(i, p))
		}
		sortStrings(parts)
		return strings.Join(parts, "; ")
	}
	return fmt.Sprint(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
