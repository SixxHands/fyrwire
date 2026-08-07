package backends

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sixxhands/fyrwire/internal/network/provider"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type apiCall struct {
	Method string
	Path   string
	Auth   string
	Body   string
}

func testDesecServer(t *testing.T, onRequest func(w http.ResponseWriter, r *http.Request)) (
	*DesecProvider, func() []apiCall) {
	t.Helper()
	var calls []apiCall
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			body := make([]byte, r.ContentLength)
			if r.ContentLength > 0 {
				r.Body.Read(body)
			}
			calls = append(calls, apiCall{r.Method, r.URL.Path,
				r.Header.Get("Authorization"), string(body)})
			onRequest(w, r)
		}))
	t.Cleanup(srv.Close)
	d := &DesecProvider{Token: "secret", Domain: "example.com",
		client: &http.Client{Timeout: 5 * time.Second}, baseUrl: srv.URL,
		minTtl: 60, minTtlAt: time.Now()}
	return d, func() []apiCall { return calls }
}

func TestAuthSchemeIsTokenNotBearer(t *testing.T) {
	d, calls := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"minimum_ttl":60}`))
	})
	d.minTtl = 0
	d.MinimumTtl()
	if got := calls()[0].Auth; got != "Token secret" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestUrlsAlwaysEndInSlash(t *testing.T) {
	d, calls := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})
	d.EnsureA("node.example.com", "1.2.3.4", 60)
	for _, c := range calls() {
		if !strings.HasSuffix(c.Path, "/") {
			t.Errorf("%s %s has no trailing slash", c.Method, c.Path)
		}
	}
}

func TestApexIsAtSignInUrlsAndEmptyInJson(t *testing.T) {
	d, calls := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(200)
	})
	d.EnsureA("example.com", "1.2.3.4", 60)
	var sawGet, sawPatch bool
	for _, c := range calls() {
		if c.Method == "GET" && strings.Contains(c.Path, "/rrsets/@/A/") {
			sawGet = true
		}
		if c.Method == "PATCH" {
			sawPatch = true
			var sets []rrset
			json.Unmarshal([]byte(c.Body), &sets)
			if len(sets) != 1 || sets[0].Subname != "" {
				t.Errorf("apex subname in JSON must be empty, got %+v", sets)
			}
		}
	}
	if !sawGet || !sawPatch {
		t.Errorf("calls: %+v", calls())
	}
}

func TestWritesGoToCollectionEndpoint(t *testing.T) {
	d, calls := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(200)
	})
	d.EnsureA("node.example.com", "1.2.3.4", 60)
	for _, c := range calls() {
		if c.Method == "PATCH" && !strings.HasSuffix(c.Path, "/rrsets/") {
			t.Errorf("PATCH went to %s, must be the collection", c.Path)
		}
	}
}

func TestNotFoundOnWriteIsAnError(t *testing.T) {
	d, _ := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"detail":"Not found."}`))
	})
	if _, err := d.read("SRV", "_mc._tcp.example.com"); err != nil {
		t.Errorf("404 on read must mean absent: %v", err)
	}
	err := d.write([]rrset{{Subname: "x", Type: "A", Ttl: 60, Records: []string{"1.2.3.4"}}})
	if err == nil {
		t.Error("404 on PATCH must be an error")
	}
}

func TestHttpsBodyFormat(t *testing.T) {
	d, calls := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(200)
	})
	d.EnsureHttps(model.HttpsRecordData{Name: "play.example.com",
		Target: "node.example.com", Port: 51471, Priority: 1, Ttl: 60})
	for _, c := range calls() {
		if c.Method != "PATCH" {
			continue
		}
		var sets []rrset
		json.Unmarshal([]byte(c.Body), &sets)
		want := "1 node.example.com. port=51471"
		if len(sets) != 1 || len(sets[0].Records) != 1 || sets[0].Records[0] != want {
			t.Errorf("records = %+v, want [%q]", sets, want)
		}
	}
}

func TestReorderedAndQuotedParamsAreNotAChange(t *testing.T) {
	d, calls := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(rrset{Ttl: 60,
				Records: []string{`1 node.example.com. port="51471"`}})
			return
		}
		w.WriteHeader(200)
	})
	what, err := d.EnsureHttps(model.HttpsRecordData{Name: "play.example.com",
		Target: "node.example.com", Port: 51471, Priority: 1, Ttl: 60})
	if err != nil {
		t.Fatal(err)
	}
	if what != "" {
		t.Errorf("unexpected change: %q", what)
	}
	for _, c := range calls() {
		if c.Method == "PATCH" {
			t.Error("wrote an unchanged record")
		}
	}
}

func TestMultiRecordRrsetIsNotRemoved(t *testing.T) {
	d, calls := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(rrset{Ttl: 60, Records: []string{
			"0 5 51471 node.example.com.", "10 0 25565 backup.example.com."}})
	})
	_, err := d.Remove("SRV", "_mc._tcp.example.com", "node.example.com")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "2 records") {
		t.Errorf("message = %v", err)
	}
	for _, c := range calls() {
		if c.Method == "DELETE" {
			t.Fatal("issued DELETE on multi-record rrset")
		}
	}
}

func TestWitnessChecksTargetNotAnyField(t *testing.T) {
	d, calls := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(rrset{Ttl: 60,
			Records: []string{"0 5 51471 somewhere-else.net."}})
	})
	if _, err := d.Remove("SRV", "_mc._tcp.example.com", "0"); err == nil {
		t.Error("priority field must not authorize removal")
	}
	for _, c := range calls() {
		if c.Method == "DELETE" {
			t.Fatal("issued DELETE on foreign record")
		}
	}
}

func TestRemoveWithoutWitnessIsRefused(t *testing.T) {
	d, _ := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {})
	_, err := d.Remove("SRV", "_mc._tcp.example.com", "")
	if err == nil || !strings.Contains(err.Error(), "witness") {
		t.Errorf("err = %v", err)
	}
}

func TestRateLimitReadsRetryAfter(t *testing.T) {
	d, _ := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		w.Write([]byte(`{"detail":"throttled"}`))
	})
	_, err := d.call("GET", d.url("domains", "example.com"), nil)
	rl, ok := err.(*provider.RateLimited)
	if !ok {
		t.Fatalf("err = %T %v", err, err)
	}
	if rl.RetryAfter != 7*time.Second {
		t.Errorf("retry after = %v", rl.RetryAfter)
	}
}

func TestTransientErrorsAreRetryableAndAuthErrorsAreNot(t *testing.T) {
	for _, c := range []struct {
		code      int
		retryable bool
	}{{503, true}, {500, true}, {409, true}, {403, false}, {401, false}, {400, false}} {
		d, _ := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
			w.Write([]byte(`{"detail":"x"}`))
		})
		_, err := d.call("GET", d.url("domains", "example.com"), nil)
		_, isRL := err.(*provider.RateLimited)
		if isRL != c.retryable {
			t.Errorf("HTTP %d: retryable=%v, want %v", c.code, isRL, c.retryable)
		}
	}
}

func TestMissingMinimumTtlIsRefused(t *testing.T) {
	d, _ := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"example.com"}`))
	})
	d.minTtl = 0
	_, err := d.MinimumTtl()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "refusing to guess") {
		t.Errorf("err = %v", err)
	}
}

func TestTtlBelowMinimumIsRefused(t *testing.T) {
	d, _ := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {})
	d.minTtl, d.minTtlAt = 3600, time.Now()
	_, err := d.EnsureA("node.example.com", "1.2.3.4", 60)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "3600") || !strings.Contains(err.Error(), "exception") {
		t.Errorf("message must name the limit and exception: %v", err)
	}
}

func TestNameOutsideDomainIsRefused(t *testing.T) {
	d, _ := testDesecServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, err := d.EnsureA("node.elsewhere.org", "1.2.3.4", 60); err == nil {
		t.Error("expected a refusal")
	}
}

func TestUrlEscapingKeepsApexAndWildcard(t *testing.T) {
	d := &DesecProvider{Domain: "example.com", baseUrl: "http://x"}
	if got := d.url("domains", "example.com", "rrsets", "@", "A"); !strings.Contains(got, "/@/") {
		t.Errorf("apex must stay literal: %s", got)
	}
	if got := d.url("rrsets", "*"); !strings.Contains(got, "/*/") {
		t.Errorf("wildcard must stay literal: %s", got)
	}
	if got := d.url("rrsets", "a?x=1#y"); strings.Contains(got, "?") || strings.Contains(got, "#") {
		t.Errorf("path characters must be escaped: %s", got)
	}
	if got := d.url("rrsets", "_minecraft._tcp.play"); !strings.Contains(got, "_minecraft._tcp.play") {
		t.Errorf("ordinary SRV labels must survive: %s", got)
	}
}
