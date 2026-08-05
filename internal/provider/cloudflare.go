package provider

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sixxhands/fyrwire/internal/config"
	"github.com/sixxhands/fyrwire/internal/model"
)

type CloudflareProvider struct {
	Token  string
	Zone   string
	client *http.Client

	ids     map[string]string
	baseURL string

	zoneName string
	zoneErr  error
	zoneOnce sync.Once
}

const cloudflareAPI = "https://api.cloudflare.com/client/v4"

// Every record we write gets stamped with this comment. Remove refuses to touch records without it so we never nuke your real DNS.
const Tag = "managed by fyrwire"

func (c *CloudflareProvider) Name() string { return "cloudflare" }

func (c *CloudflareProvider) checkZone(name string) error {
	c.zoneOnce.Do(func() {
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

			c.zoneErr = model.Errorf(
				"cloudflare zone %s is %q, not active — records written to it "+
					"will be accepted and will never resolve until the "+
					"nameservers are delegated", z.Name, z.Status)
		}
	})
	if c.zoneErr != nil {
		return c.zoneErr
	}
	if c.zoneName == "" {
		return nil
	}
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	if n != c.zoneName && !strings.HasSuffix(n, "."+c.zoneName) {
		return model.Errorf(
			"%q is not inside the zone %q that zone_id points at — the record "+
				"would be accepted and would never resolve", name, c.zoneName)
	}
	return nil
}

func (c *CloudflareProvider) api() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return cloudflareAPI
}

type cfEnvelope struct {
	Success bool            `json:"success"`
	Errors  []cfError       `json:"errors"`
	Result  json.RawMessage `json:"result"`
	Extra   map[string]any  `json:"-"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *CloudflareProvider) call(method, path string, body any) (json.RawMessage, error) {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}

	code, data, hdr, err := httpDo(c.client, method, c.api()+path, payload,
		map[string]string{"Authorization": "Bearer " + c.Token})
	if err != nil {
		return nil, model.Errorf("cloudflare %s", err)
	}
	var env cfEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, model.Errorf("cloudflare sent unreadable JSON: %v", err)
	}
	if !env.Success {
		for _, e := range env.Errors {
			if e.Code == 971 || strings.Contains(strings.ToLower(e.Message), "rate") {
				return nil, &RateLimited{
					Msg:        "cloudflare rate limit on " + method + " " + path,
					RetryAfter: retryAfter(hdr)}
			}
		}
		if code == 401 || code == 403 {
			return nil, model.Errorf(
				"cloudflare refused the token: %s. This is a permissions "+
					"problem, not a transient one.", cfErrText(env.Errors))
		}
		if code == 409 || code == 500 || code == 502 || code == 503 || code == 504 {
			return nil, &RateLimited{Msg: model.Errorf(
				"cloudflare temporary failure (HTTP %d): %s", code,
				cfErrText(env.Errors)).Error()}
		}
		return nil, model.Errorf("cloudflare rejected %s %s: %s", method, path,
			cfErrText(env.Errors))
	}
	return env.Result, nil
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
	ID      string         `json:"id,omitempty"`
	Type    string         `json:"type"`
	Name    string         `json:"name"`
	TTL     int            `json:"ttl"`
	Comment string         `json:"comment,omitempty"`
	Content string         `json:"content,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

func (c *CloudflareProvider) find(rtype, name string) (*cfRecord, error) {
	raw, err := c.call("GET", "/zones/"+c.Zone+"/dns_records?type="+
		url.QueryEscape(rtype)+"&name="+url.QueryEscape(name), nil)
	if err != nil {
		return nil, err
	}
	var recs []cfRecord
	if err := json.Unmarshal(raw, &recs); err != nil {

		return nil, model.Errorf(
			"cloudflare sent an unreadable record list: %v", err)
	}
	if len(recs) == 0 {
		return nil, nil
	}
	return &recs[0], nil
}

func (c *CloudflareProvider) upsert(rec cfRecord) (string, error) {
	if err := c.checkZone(rec.Name); err != nil {
		return "", err
	}
	key := rec.Type + "|" + rec.Name
	existing, err := c.find(rec.Type, rec.Name)
	if err != nil {
		return "", err
	}
	if existing != nil && c.matches(existing, rec) {
		return "", nil
	}
	rec.Comment = Tag
	if existing != nil {
		if _, err := c.call("PUT",
			"/zones/"+c.Zone+"/dns_records/"+existing.ID, rec); err != nil {
			return "", err
		}
		c.ids[key] = existing.ID
		return "updated " + rec.Type + " " + rec.Name, nil
	}
	raw, err := c.call("POST", "/zones/"+c.Zone+"/dns_records", rec)
	if err != nil {
		return "", err
	}
	var created cfRecord
	if json.Unmarshal(raw, &created) == nil {
		c.ids[key] = created.ID
	}
	return "created " + rec.Type + " " + rec.Name, nil
}

func (c *CloudflareProvider) matches(existing *cfRecord, want cfRecord) bool {

	if !strings.EqualFold(strings.TrimSuffix(existing.Name, "."),
		strings.TrimSuffix(want.Name, ".")) {
		return false
	}
	if existing.Type != want.Type {
		return false
	}
	if existing.TTL != want.TTL {
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
			if !sameHost(toString(got), toString(v)) {
				return false
			}
			continue
		}
		if toString(got) != toString(v) {
			return false
		}
	}
	return true
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strings.TrimSuffix(strings.TrimRight(
			jsonNumber(t), "."), ".")
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func (c *CloudflareProvider) EnsureA(name, ip string, ttl int) (string, error) {
	what, err := c.upsert(cfRecord{Type: "A", Name: name, TTL: ttl, Content: ip})
	if what == "" || err != nil {
		return "", err
	}
	return what + " -> " + ip, nil
}

func (c *CloudflareProvider) EnsureSRV(rec model.SrvRecordData) (string, error) {
	if err := CheckTarget(rec.Target); err != nil {
		return "", err
	}
	what, err := c.upsert(cfRecord{Type: "SRV", Name: rec.Name, TTL: rec.TTL,
		Data: map[string]any{"priority": rec.Priority, "weight": rec.Weight,
			"port": rec.Port, "target": rec.Target}})
	if what == "" || err != nil {
		return "", err
	}
	return what + " -> " + rec.Value(), nil
}

func (c *CloudflareProvider) EnsureHTTPS(rec model.HttpsRecordData) (string, error) {
	if err := CheckTarget(rec.Target); err != nil {
		return "", err
	}

	what, err := c.upsert(cfRecord{Type: "HTTPS", Name: rec.Name, TTL: rec.TTL,
		Data: map[string]any{"priority": rec.Priority, "target": rec.Target,
			"value": "port=" + itoa(rec.Port)}})
	if what == "" || err != nil {
		return "", err
	}
	return what + " -> " + rec.Value(), nil
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func (c *CloudflareProvider) Delete(rtype, name, expect string) (string, error) {
	existing, err := c.find(rtype, name)
	if err != nil {
		return "", err
	}
	if existing == nil {
		return "", nil
	}

	if !strings.Contains(existing.Comment, Tag) {
		return "", model.Errorf(
			"%s %s is not tagged %q — refusing to delete a record FyrWire did "+
				"not create", rtype, name, Tag)
	}
	if expect != "" {
		actual := toString(existing.Data["target"])
		if actual == "" {
			actual = existing.Content
		}
		if !sameHost(actual, expect) {
			return "", model.Errorf(
				"%s %s points at %q, not the expected %q — refusing to delete a "+
					"record that is not ours", rtype, name, actual, expect)
		}
	}
	if _, err := c.call("DELETE",
		"/zones/"+c.Zone+"/dns_records/"+existing.ID, nil); err != nil {
		return "", err
	}
	return "deleted " + rtype + " " + name, nil
}

func init() {
	register("cloudflare", []config.OptionRule{
		{Name: "api_token", Required: true, Kind: "string"},
		{Name: "zone_id", Required: true, Kind: "string"},
		{Name: "timeout", Kind: "int"},
	}, func(sec config.Section) (IDnsProvider, error) {
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
		return &CloudflareProvider{Token: token, Zone: zone, ids: map[string]string{},
			client: &http.Client{
				Timeout: time.Duration(sec.Int("timeout", 10)) * time.Second}}, nil
	})
}
