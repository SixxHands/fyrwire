package backends

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sixxhands/fyrwire/internal/network/provider"
	"github.com/sixxhands/fyrwire/internal/node/config"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type DesecProvider struct {
	Token       string
	Domain      string
	client      *http.Client
	minTtl      int
	minTtlAt    time.Time
	ttlCacheFor time.Duration
	baseUrl     string
}

const desecApi = "https://desec.io/api/v1"

var temporaryStatusCodes = map[int]bool{
	409: true,
	500: true,
	502: true,
	503: true,
	504: true,
}

func escapeSegment(value string) string {
	const safe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~@*"
	var builder strings.Builder
	for i := 0; i < len(value); i++ {
		char := value[i]
		if strings.IndexByte(safe, char) >= 0 {
			builder.WriteByte(char)
		} else {
			builder.WriteString(fmt.Sprintf("%%%02X", char))
		}
	}

	return builder.String()
}

func (desec *DesecProvider) Name() string {
	return "desec"
}

func (desec *DesecProvider) api() string {
	if desec.baseUrl != "" {
		return desec.baseUrl
	}

	return desecApi
}

func (desec *DesecProvider) url(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		clean = append(clean, escapeSegment(strings.TrimSpace(part)))
	}

	return desec.api() + "/" + strings.Join(clean, "/") + "/"
}

func (desec *DesecProvider) call(method, requestUrl string, body any) ([]byte, error) {
	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = json.Marshal(body)
	}

	code, data, headers, err := provider.SendHttp(desec.client, method, requestUrl, bodyBytes,
		map[string]string{"Authorization": "Token " + desec.Token})
	if err != nil {
		return nil, model.Errorf("desec %s", err)
	}

	return checkDesecResponse(method, code, data, headers)
}

func checkDesecResponse(method string, code int, data []byte, headers http.Header) ([]byte, error) {
	if code >= 200 && code < 300 {
		return data, nil
	}

	if code == 404 && (method == "GET" || method == "DELETE") {
		return nil, nil
	}

	if code == 429 {
		return nil, &provider.RateLimited{
			Msg:        "desec rate limit: " + provider.JsonOr(data, "throttled"),
			RetryAfter: provider.RetryAfter(headers),
		}
	}

	if code == 401 || code == 403 {
		return nil, model.Errorf("desec refused the token (HTTP %d): %s", code, provider.JsonOr(data, "forbidden"))
	}

	if temporaryStatusCodes[code] {
		message := model.Errorf("desec temporary failure (HTTP %d): %s", code, provider.JsonOr(data, "server error"))
		return nil, &provider.RateLimited{Msg: message.Error()}
	}

	return nil, model.Errorf("desec %s -> HTTP %d: %s", method, code, provider.JsonOr(data, "error"))
}

func (desec *DesecProvider) MinimumTtl() (int, error) {
	cacheFor := desec.ttlCacheFor
	if cacheFor == 0 {
		cacheFor = time.Hour
	}

	if desec.minTtl != 0 && time.Since(desec.minTtlAt) < cacheFor {
		return desec.minTtl, nil
	}

	data, err := desec.call("GET", desec.url("domains", desec.Domain), nil)
	if err != nil {
		return 0, err
	}
	if data == nil {
		return 0, model.Errorf("desec has no domain named %q on this account", desec.Domain)
	}

	var domainInfo struct {
		MinimumTtl *int `json:"minimum_ttl"`
	}
	if err := json.Unmarshal(data, &domainInfo); err != nil {
		return 0, model.Errorf("desec sent an unreadable domain record: %v", err)
	}
	if domainInfo.MinimumTtl == nil {
		return 0, model.Errorf("desec did not report minimum_ttl for %s; refusing to guess", desec.Domain)
	}

	desec.minTtl = *domainInfo.MinimumTtl
	desec.minTtlAt = time.Now()
	return desec.minTtl, nil
}

// deSEC enforces a strict per-domain minimum TTL (usually 3600s) and rejects anything lower. checks it upfront.
func (desec *DesecProvider) checkTtl(ttl int) error {
	minAllowed, err := desec.MinimumTtl()
	if err != nil {
		return err
	}

	if ttl < minAllowed {
		return model.Errorf(
			"desec enforces a minimum TTL of %ds on %s, but node asks for %ds; "+
				"set node.ttl = %d or request a lower-TTL exception from deSEC",
			minAllowed, desec.Domain, ttl, minAllowed,
		)
	}

	return nil
}

func (desec *DesecProvider) subname(name string) (string, error) {
	trimmed := strings.ToLower(strings.Trim(strings.TrimSpace(name), "."))
	if trimmed == desec.Domain {
		return "", nil
	}

	suffix := "." + desec.Domain
	if !strings.HasSuffix(trimmed, suffix) {
		return "", model.Errorf("%q is not inside %q", name, desec.Domain)
	}

	return strings.TrimSuffix(trimmed, suffix), nil
}

type rrset struct {
	Subname string   `json:"subname"`
	Type    string   `json:"type"`
	Ttl     int      `json:"ttl"`
	Records []string `json:"records"`
}

func (desec *DesecProvider) read(recordType, name string) (*rrset, error) {
	subname, err := desec.subname(name)
	if err != nil {
		return nil, err
	}

	segment := subname
	if segment == "" {
		segment = "@"
	}

	data, err := desec.call("GET", desec.url("domains", desec.Domain, "rrsets", segment, recordType), nil)
	if err != nil || data == nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}

	var result rrset
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, model.Errorf("desec sent an unreadable rrset: %v", err)
	}

	return &result, nil
}

func (desec *DesecProvider) write(sets []rrset) error {
	if len(sets) == 0 {
		return nil
	}

	_, err := desec.call("PATCH", desec.url("domains", desec.Domain, "rrsets"), sets)
	return err
}

func (desec *DesecProvider) same(recordType string, existing *rrset, records []string, ttl int) bool {
	if existing == nil || existing.Ttl != ttl || len(existing.Records) != len(records) {
		return false
	}

	have := make([]string, 0, len(existing.Records))
	for _, record := range existing.Records {
		have = append(have, provider.StandardValue(recordType, record))
	}

	want := make([]string, 0, len(records))
	for _, record := range records {
		want = append(want, provider.StandardValue(recordType, record))
	}

	sort.Strings(have)
	sort.Strings(want)
	for i := range have {
		if have[i] != want[i] {
			return false
		}
	}

	return true
}

func (desec *DesecProvider) ensure(recordType, name string, records []string, ttl int) (string, error) {
	if err := desec.checkTtl(ttl); err != nil {
		return "", err
	}

	existing, err := desec.read(recordType, name)
	if err != nil {
		return "", err
	}
	if desec.same(recordType, existing, records, ttl) {
		return "", nil
	}

	subname, err := desec.subname(name)
	if err != nil {
		return "", err
	}

	verb := "created"
	if existing != nil {
		verb = "updated"
	}

	if err := desec.write([]rrset{{Subname: subname, Type: recordType, Ttl: ttl, Records: records}}); err != nil {
		return "", err
	}

	return verb + " " + recordType + " " + name, nil
}

func (desec *DesecProvider) EnsureA(name, ip string, ttl int) (string, error) {
	change, err := desec.ensure("A", name, []string{ip}, ttl)
	if change == "" || err != nil {
		return "", err
	}

	return change + " -> " + ip, nil
}

func (desec *DesecProvider) EnsureSrv(record model.SrvRecordData) (string, error) {
	if err := provider.CheckTarget(record.Target); err != nil {
		return "", err
	}

	value := model.SrvRecordData{
		Name: record.Name, Target: strings.TrimSuffix(record.Target, ".") + ".",
		Port: record.Port, Priority: record.Priority, Weight: record.Weight,
	}.Value()

	change, err := desec.ensure("SRV", record.Name, []string{value}, record.Ttl)
	if change == "" || err != nil {
		return "", err
	}

	return change + " -> " + record.Value(), nil
}

func (desec *DesecProvider) EnsureHttps(record model.HttpsRecordData) (string, error) {
	if err := provider.CheckTarget(record.Target); err != nil {
		return "", err
	}

	value := model.HttpsRecordData{
		Target: strings.TrimSuffix(record.Target, ".") + ".",
		Port:   record.Port, Priority: record.Priority,
	}.Value()

	change, err := desec.ensure("HTTPS", record.Name, []string{value}, record.Ttl)
	if change == "" || err != nil {
		return "", err
	}

	return change + " -> " + record.Value(), nil
}

func (desec *DesecProvider) Remove(recordType, name, expect string) (string, error) {
	if expect == "" {
		return "", model.Errorf("refusing to remove %s %s on desec without an ownership witness",
			recordType, name)
	}

	existing, err := desec.read(recordType, name)
	if err != nil || existing == nil || len(existing.Records) == 0 {
		return "", err
	}

	if len(existing.Records) > 1 {
		return "", model.Errorf(
			"%s %s has %d records and deSEC removes the whole set; remove manually:\n  %s",
			recordType, name, len(existing.Records), strings.Join(existing.Records, "\n  "))
	}

	actual := provider.TargetOf(recordType, existing.Records[0])
	if !provider.SameHost(actual, expect) {
		return "", model.Errorf("%s %s points at %q, not %q; refusing to remove",
			recordType, name, actual, expect)
	}

	subname, err := desec.subname(name)
	if err != nil {
		return "", err
	}

	segment := subname
	if segment == "" {
		segment = "@"
	}

	requestUrl := desec.url("domains", desec.Domain, "rrsets", segment, recordType)
	if _, err := desec.call("DELETE", requestUrl, nil); err != nil {
		return "", err
	}

	return "removed " + recordType + " " + name, nil
}

func init() {
	provider.Register("desec", []config.OptionRule{
		{Name: "token", Required: true, Kind: "string"},
		{Name: "domain", Required: true, Kind: "string"},
		{Name: "timeout", Kind: "int"},
	}, buildDesecProvider)
}

func buildDesecProvider(section config.Section) (provider.IDnsProvider, error) {
	token, err := section.String("token")
	if err != nil {
		return nil, err
	}

	domain, err := section.String("domain")
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
			Timeout: time.Duration(section.Int("timeout", 10)) * time.Second,
		},
	}, nil
}
