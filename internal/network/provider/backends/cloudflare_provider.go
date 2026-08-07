package backends

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sixxhands/fyrwire/internal/network/provider"
	"github.com/sixxhands/fyrwire/internal/node/config"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type CloudflareProvider struct {
	Token  string
	Zone   string
	client *http.Client

	baseUrl  string
	zoneName string
	zoneErr  error
	zoneOnce sync.Once
}

const cloudflareApi = "https://api.cloudflare.com/client/v4"

// Every record we write gets stamped with this comment. Remove refuses to touch records without it so we never nuke your real DNS.
const Tag = "managed by fyrwire"

func (c *CloudflareProvider) Name() string { return "cloudflare" }

func (c *CloudflareProvider) checkZone(name string) error {
	c.zoneOnce.Do(c.loadZone)
	if c.zoneErr != nil {
		return c.zoneErr
	}
	if c.zoneName == "" {
		return nil
	}
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	if n != c.zoneName && !strings.HasSuffix(n, "."+c.zoneName) {
		return model.Errorf("%q is not inside zone %q", name, c.zoneName)
	}
	return nil
}

func (c *CloudflareProvider) loadZone() {
	raw, err := c.call("GET", "/zones/"+c.Zone, nil)
	if err != nil {
		c.zoneErr = err
		return
	}
	var z struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &z); err != nil {
		c.zoneErr = model.Errorf("cloudflare sent an unreadable zone: %v", err)
		return
	}
	c.zoneName = strings.ToLower(z.Name)
	if z.Status != "" && z.Status != "active" {
		c.zoneErr = model.Errorf("cloudflare zone %s is %q, not active", z.Name, z.Status)
	}
}

func (c *CloudflareProvider) api() string {
	if c.baseUrl != "" {
		return c.baseUrl
	}
	return cloudflareApi
}

type cfEnvelope struct {
	Success bool            `json:"success"`
	Errors  []cfError       `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *CloudflareProvider) call(method, path string, body any) (json.RawMessage, error) {
	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = json.Marshal(body)
	}
	code, data, headers, err := provider.SendHttp(c.client, method, c.api()+path, bodyBytes,
		map[string]string{"Authorization": "Bearer " + c.Token})
	if err != nil {
		return nil, model.Errorf("cloudflare %s", err)
	}
	var env cfEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, model.Errorf("cloudflare sent unreadable JSON: %v", err)
	}
	if !env.Success {
		return nil, classifyCloudflareError(method, path, code, headers, env.Errors)
	}
	return env.Result, nil
}

func classifyCloudflareError(method, path string, code int, headers http.Header, errs []cfError) error {
	for _, e := range errs {
		if e.Code == 971 || strings.Contains(strings.ToLower(e.Message), "rate") {
			return &provider.RateLimited{
				Msg:        "cloudflare rate limit on " + method + " " + path,
				RetryAfter: provider.RetryAfter(headers),
			} // sayonara cloudflare
		}
	}
	if code == 401 || code == 403 {
		return model.Errorf("cloudflare refused the token: %s", cfErrText(errs))
	}
	if code == 409 || code == 500 || code == 502 || code == 503 || code == 504 {
		return &provider.RateLimited{Msg: model.Errorf(
			"cloudflare temporary failure (HTTP %d): %s", code, cfErrText(errs)).Error()}
	}
	return model.Errorf("cloudflare rejected %s %s: %s", method, path, cfErrText(errs))
}

func cfErrText(errs []cfError) string {
	if len(errs) == 0 {
		return "unknown error"
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, e.Message)
	}
	return strings.Join(parts, "; ")
}

type cfRecord struct {
	Id      string         `json:"id,omitempty"`
	Type    string         `json:"type"`
	Name    string         `json:"name"`
	Ttl     int            `json:"ttl"`
	Comment string         `json:"comment,omitempty"`
	Content string         `json:"content,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

func (c *CloudflareProvider) find(recordType, name string) (*cfRecord, error) {
	raw, err := c.call("GET", "/zones/"+c.Zone+"/dns_records?type="+
		url.QueryEscape(recordType)+"&name="+url.QueryEscape(name), nil)
	if err != nil {
		return nil, err
	}
	var records []cfRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, model.Errorf("cloudflare sent an unreadable record list: %v", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	return &records[0], nil
}

func (c *CloudflareProvider) upsert(record cfRecord) (string, error) {
	if err := c.checkZone(record.Name); err != nil {
		return "", err
	}
	existing, err := c.find(record.Type, record.Name)
	if err != nil {
		return "", err
	}
	if existing != nil && c.matches(existing, record) {
		return "", nil
	}
	record.Comment = Tag
	if existing != nil {
		if _, err := c.call("PUT", "/zones/"+c.Zone+"/dns_records/"+existing.Id, record); err != nil {
			return "", err
		}
		return "updated " + record.Type + " " + record.Name, nil
	}
	if _, err := c.call("POST", "/zones/"+c.Zone+"/dns_records", record); err != nil {
		return "", err
	}
	return "created " + record.Type + " " + record.Name, nil
}

func (c *CloudflareProvider) matches(existing *cfRecord, want cfRecord) bool {
	if !strings.EqualFold(strings.TrimSuffix(existing.Name, "."),
		strings.TrimSuffix(want.Name, ".")) {
		return false
	}
	if existing.Type != want.Type || existing.Ttl != want.Ttl {
		return false
	}
	if want.Data == nil {
		return existing.Content == want.Content
	}
	for k, v := range want.Data {
		got, ok := existing.Data[k]
		if !ok {
			return false
		}
		if k == "target" {
			if !provider.SameHost(valueText(got), valueText(v)) {
				return false
			}
			continue
		}
		if valueText(got) != valueText(v) {
			return false
		}
	}
	return true
}

func valueText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (c *CloudflareProvider) EnsureA(name, ip string, ttl int) (string, error) {
	what, err := c.upsert(cfRecord{Type: "A", Name: name, Ttl: ttl, Content: ip})
	if what == "" || err != nil {
		return "", err
	}
	return what + " -> " + ip, nil
}

func (c *CloudflareProvider) EnsureSrv(record model.SrvRecordData) (string, error) {
	if err := provider.CheckTarget(record.Target); err != nil {
		return "", err
	}
	what, err := c.upsert(cfRecord{Type: "SRV", Name: record.Name, Ttl: record.Ttl,
		Data: map[string]any{"priority": record.Priority, "weight": record.Weight,
			"port": record.Port, "target": record.Target}})
	if what == "" || err != nil {
		return "", err
	}
	return what + " -> " + record.Value(), nil
}

func (c *CloudflareProvider) EnsureHttps(record model.HttpsRecordData) (string, error) {
	if err := provider.CheckTarget(record.Target); err != nil {
		return "", err
	}
	what, err := c.upsert(cfRecord{Type: "HTTPS", Name: record.Name, Ttl: record.Ttl,
		Data: map[string]any{"priority": record.Priority, "target": record.Target,
			"value": "port=" + strconv.Itoa(record.Port)}})
	if what == "" || err != nil {
		return "", err
	}
	return what + " -> " + record.Value(), nil
}

func (c *CloudflareProvider) Remove(recordType, name, expect string) (string, error) {
	existing, err := c.find(recordType, name)
	if err != nil || existing == nil {
		return "", err
	}
	if !strings.Contains(existing.Comment, Tag) {
		return "", model.Errorf("%s %s is not tagged %q; refusing to remove", recordType, name, Tag)
	}
	if expect != "" {
		actual := valueText(existing.Data["target"])
		if actual == "" {
			actual = existing.Content
		}
		if !provider.SameHost(actual, expect) {
			return "", model.Errorf("%s %s points at %q, not %q; refusing to remove",
				recordType, name, actual, expect)
		}
	}
	if _, err := c.call("DELETE", "/zones/"+c.Zone+"/dns_records/"+existing.Id, nil); err != nil {
		return "", err
	}
	return "removed " + recordType + " " + name, nil
}

func init() {
	provider.Register("cloudflare", []config.OptionRule{
		{Name: "api_token", Required: true, Kind: "string"},
		{Name: "zone_id", Required: true, Kind: "string"},
		{Name: "timeout", Kind: "int"},
	}, func(sec config.Section) (provider.IDnsProvider, error) {
		token, err := sec.String("api_token")
		if err != nil {
			return nil, err
		}
		zone, err := sec.String("zone_id")
		if err != nil {
			return nil, err
		}
		if token == "" || zone == "" {
			return nil, model.Errorf("cloudflare provider needs api_token and zone_id")
		}
		return &CloudflareProvider{Token: token, Zone: zone,
			client: &http.Client{
				Timeout: time.Duration(sec.Int("timeout", 10)) * time.Second}}, nil
	})
}
