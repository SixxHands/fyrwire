package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sixxhands/fyrwire/internal/config"
	"github.com/sixxhands/fyrwire/internal/model"
)

type DesecProvider struct {
	Token  string
	Domain string
	client *http.Client

	minTTL   int
	minTTLAt time.Time

	ttlCacheFor time.Duration

	baseURL string
}

const desecAPI = "https://desec.io/api/v1"

func escapeSegment(s string) string {
	const safe = "abcdefghijklmnopqrstuvwxyz" +
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~@*"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}

func (d *DesecProvider) Name() string { return "desec" }

func (d *DesecProvider) api() string {
	if d.baseURL != "" {
		return d.baseURL
	}
	return desecAPI
}

func (d *DesecProvider) url(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, p := range parts {
		e := escapeSegment(strings.TrimSpace(p))
		clean = append(clean, e)
	}
	return d.api() + "/" + strings.Join(clean, "/") + "/"
}

func (d *DesecProvider) call(method, u string, body any) ([]byte, error) {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	code, data, hdr, err := httpDo(d.client, method, u, payload,
		map[string]string{"Authorization": "Token " + d.Token})
	if err != nil {
		return nil, model.Errorf("desec %s", err)
	}
	switch {
	case code >= 200 && code < 300:
		return data, nil
	case code == 404 && (method == "GET" || method == "DELETE"):

		return nil, nil
	case code == 429:
		return nil, &RateLimited{
			Msg:        "desec rate limit: " + jsonOr(data, "throttled"),
			RetryAfter: retryAfter(hdr)}
	case code == 401 || code == 403:
		return nil, model.Errorf(
			"desec refused the token (HTTP %d): %s. This is a permissions "+
				"problem, not a transient one — retrying will not help.",
			code, jsonOr(data, "forbidden"))
	case code == 409 || code == 500 || code == 502 || code == 503 || code == 504:

		return nil, &RateLimited{
			Msg: model.Errorf("desec temporary failure (HTTP %d): %s",
				code, jsonOr(data, "server error")).Error()}
	}
	return nil, model.Errorf("desec %s -> HTTP %d: %s", method, code,
		jsonOr(data, "error"))
}

func (d *DesecProvider) MinimumTTL() (int, error) {
	cacheFor := d.ttlCacheFor
	if cacheFor == 0 {
		cacheFor = time.Hour
	}
	if d.minTTL != 0 && time.Since(d.minTTLAt) < cacheFor {
		return d.minTTL, nil
	}
	data, err := d.call("GET", d.url("domains", d.Domain), nil)
	if err != nil {
		return 0, err
	}
	if data == nil {
		return 0, model.Errorf(
			"desec does not have a domain named %q on this account — check the "+
				"name, or add it at desec.io first", d.Domain)
	}
	var dom struct {
		MinimumTTL *int `json:"minimum_ttl"`
	}
	if err := json.Unmarshal(data, &dom); err != nil {
		return 0, model.Errorf("desec sent an unreadable domain record: %v", err)
	}
	if dom.MinimumTTL == nil {

		return 0, model.Errorf(
			"desec did not report a minimum_ttl for %s — the API may have "+
				"changed. Refusing to guess.", d.Domain)
	}
	d.minTTL, d.minTTLAt = *dom.MinimumTTL, time.Now()
	return d.minTTL, nil
}

// deSEC enforces a strict per-domain minimum TTL (usually 3600s) and rejects anything lower, so check it upfront.
func (d *DesecProvider) checkTTL(ttl int) error {
	floor, err := d.MinimumTTL()
	if err != nil {
		return err
	}
	if ttl < floor {
		return model.Errorf(
			"desec enforces a minimum TTL of %ds on %s, but this node asks for "+
				"%ds.\n  A %ds TTL means clients can cache a stale port for that "+
				"long after a rotation.\n  Either set the node's ttl to %d, or ask "+
				"deSEC support for a lower-TTL exception (they grant them on "+
				"request).", floor, d.Domain, ttl, floor, floor)
	}
	return nil
}

func (d *DesecProvider) subname(name string) (string, error) {
	n := strings.ToLower(strings.Trim(strings.TrimSpace(name), "."))
	if n == d.Domain {
		return "", nil
	}
	suffix := "." + d.Domain
	if !strings.HasSuffix(n, suffix) {
		return "", model.Errorf(
			"%q is not inside %q — the desec provider is scoped to one domain, "+
				"so it cannot write that name", name, d.Domain)
	}
	return strings.TrimSuffix(n, suffix), nil
}

type rrset struct {
	Subname string   `json:"subname"`
	Type    string   `json:"type"`
	TTL     int      `json:"ttl"`
	Records []string `json:"records"`
}

func (d *DesecProvider) read(rtype, name string) (*rrset, error) {
	sub, err := d.subname(name)
	if err != nil {
		return nil, err
	}
	seg := sub
	if seg == "" {
		seg = "@"
	}
	data, err := d.call("GET", d.url("domains", d.Domain, "rrsets", seg, rtype), nil)
	if err != nil || data == nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var out rrset
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, model.Errorf("desec sent an unreadable rrset: %v", err)
	}
	return &out, nil
}

func (d *DesecProvider) write(sets []rrset) error {
	if len(sets) == 0 {
		return nil
	}
	_, err := d.call("PATCH", d.url("domains", d.Domain, "rrsets"), sets)
	return err
}

func (d *DesecProvider) same(rtype string, existing *rrset, records []string, ttl int) bool {
	if existing == nil || existing.TTL != ttl ||
		len(existing.Records) != len(records) {
		return false
	}
	have := make([]string, 0, len(existing.Records))
	for _, r := range existing.Records {
		have = append(have, Canon(rtype, r))
	}
	want := make([]string, 0, len(records))
	for _, r := range records {
		want = append(want, Canon(rtype, r))
	}
	sortStrings(have)
	sortStrings(want)
	for i := range have {
		if have[i] != want[i] {
			return false
		}
	}
	return true
}

func (d *DesecProvider) ensure(rtype, name string, records []string, ttl int) (string, error) {
	if err := d.checkTTL(ttl); err != nil {
		return "", err
	}
	existing, err := d.read(rtype, name)
	if err != nil {
		return "", err
	}
	if d.same(rtype, existing, records, ttl) {
		return "", nil
	}
	sub, err := d.subname(name)
	if err != nil {
		return "", err
	}
	verb := "created"
	if existing != nil {
		verb = "updated"
	}
	if err := d.write([]rrset{{Subname: sub, Type: rtype, TTL: ttl,
		Records: records}}); err != nil {
		return "", err
	}
	return verb + " " + rtype + " " + name, nil
}

func (d *DesecProvider) EnsureA(name, ip string, ttl int) (string, error) {
	what, err := d.ensure("A", name, []string{ip}, ttl)
	if what == "" || err != nil {
		return "", err
	}
	return what + " -> " + ip, nil
}

func (d *DesecProvider) EnsureSRV(rec model.SrvRecordData) (string, error) {
	if err := CheckTarget(rec.Target); err != nil {
		return "", err
	}
	value := model.SrvRecordData{Name: rec.Name,
		Target: strings.TrimSuffix(rec.Target, ".") + ".", Port: rec.Port,
		Priority: rec.Priority, Weight: rec.Weight}.Value()
	what, err := d.ensure("SRV", rec.Name, []string{value}, rec.TTL)
	if what == "" || err != nil {
		return "", err
	}
	return what + " -> " + rec.Value(), nil
}

func (d *DesecProvider) EnsureHTTPS(rec model.HttpsRecordData) (string, error) {
	if err := CheckTarget(rec.Target); err != nil {
		return "", err
	}
	value := model.HttpsRecordData{Target: strings.TrimSuffix(rec.Target, ".") + ".",
		Port: rec.Port, Priority: rec.Priority}.Value()
	what, err := d.ensure("HTTPS", rec.Name, []string{value}, rec.TTL)
	if what == "" || err != nil {
		return "", err
	}
	return what + " -> " + rec.Value(), nil
}

func (d *DesecProvider) Delete(rtype, name, expect string) (string, error) {

	if expect == "" {
		return "", model.Errorf(
			"refusing to delete %s %s on desec without an ownership witness — "+
				"desec has no record metadata, so the only safe proof is that "+
				"the record points where FyrWire published", rtype, name)
	}
	existing, err := d.read(rtype, name)
	if err != nil {
		return "", err
	}
	if existing == nil || len(existing.Records) == 0 {
		return "", nil
	}

	if len(existing.Records) > 1 {
		return "", model.Errorf(
			"%s %s holds %d records, and deSEC can only delete the whole set. "+
				"Removing ours would take the others with it, so this has to be "+
				"done by hand:\n  %s", rtype, name, len(existing.Records),
			strings.Join(existing.Records, "\n  "))
	}

	actual := TargetOf(rtype, existing.Records[0])
	if !sameHost(actual, expect) {
		return "", model.Errorf(
			"%s %s points at %q, not the expected %q — refusing to delete a "+
				"record that is not ours", rtype, name, actual, expect)
	}
	sub, err := d.subname(name)
	if err != nil {
		return "", err
	}
	seg := sub
	if seg == "" {
		seg = "@"
	}
	if _, err := d.call("DELETE",
		d.url("domains", d.Domain, "rrsets", seg, rtype), nil); err != nil {
		return "", err
	}
	return "deleted " + rtype + " " + name, nil
}

func init() {
	register("desec", []config.OptionRule{
		{Name: "token", Required: true, Kind: "string"},
		{Name: "domain", Required: true, Kind: "string"},
		{Name: "timeout", Kind: "int"},
	}, func(sec config.Section) (IDnsProvider, error) {
		token, err := sec.String("token")
		if err != nil {
			return nil, err
		}
		domain, err := sec.String("domain")
		if err != nil {
			return nil, err
		}
		if token == "" || domain == "" {
			return nil, model.Errorf("desec provider needs token and domain")
		}
		return &DesecProvider{
			Token:  token,
			Domain: strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), ".")),
			client: &http.Client{
				Timeout: time.Duration(sec.Int("timeout", 10)) * time.Second},
		}, nil
	})
}
