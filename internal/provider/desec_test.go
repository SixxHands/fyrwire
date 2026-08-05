package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sixxhands/fyrwire/internal/model"
)

type call struct {
	Method string
	Path   string
	Auth   string
	Body   string
}

func server(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) (
	*DesecProvider, func() []call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			body := make([]byte, r.ContentLength)
			if r.ContentLength > 0 {
				r.Body.Read(body)
			}
			calls = append(calls, call{r.Method, r.URL.Path,
				r.Header.Get("Authorization"), string(body)})
			handle(w, r)
		}))
	t.Cleanup(srv.Close)
	d := &DesecProvider{Token: "secret", Domain: "example.com",
		client: &http.Client{Timeout: 5 * time.Second}, baseURL: srv.URL,
		minTTL: 60, minTTLAt: time.Now()}
	return d, func() []call { return calls }
}

func TestAuthSchemeIsTokenNotBearer(t *testing.T) {

	d, calls := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"minimum_ttl":60}`))
	})
	d.minTTL = 0
	d.MinimumTTL()
	if got := calls()[0].Auth; got != "Token secret" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestUrlsAlwaysEndInASlash(t *testing.T) {

	d, calls := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})
	d.EnsureA("node.example.com", "1.2.3.4", 60)
	for _, c := range calls() {
		if !strings.HasSuffix(c.Path, "/") {
			t.Errorf("%s %s has no trailing slash", c.Method, c.Path)
		}
	}
}

func TestApexIsAtSignInUrlsButEmptyInJson(t *testing.T) {
	d, calls := server(t, func(w http.ResponseWriter, r *http.Request) {
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

func TestWritesGoToTheCollectionNotTheDetailEndpoint(t *testing.T) {
	d, calls := server(t, func(w http.ResponseWriter, r *http.Request) {
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

func TestA404OnAWriteIsNotReportedAsSuccess(t *testing.T) {
	d, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"detail":"Not found."}`))
	})

	if _, err := d.read("SRV", "_mc._tcp.example.com"); err != nil {
		t.Errorf("a 404 on a read must mean absent: %v", err)
	}

	err := d.write([]rrset{{Subname: "x", Type: "A", TTL: 60,
		Records: []string{"1.2.3.4"}}})
	if err == nil {
		t.Error("a 404 on PATCH must be an error, not a silent success")
	}
}

func TestHttpsBodyIsExactlyRight(t *testing.T) {
	d, calls := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(200)
	})
	d.EnsureHTTPS(model.HttpsRecordData{Name: "play.example.com",
		Target: "node.example.com", Port: 51471, Priority: 1, TTL: 60})
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
	d, calls := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(rrset{TTL: 60,
				Records: []string{`1 node.example.com. port="51471"`}})
			return
		}
		w.WriteHeader(200)
	})
	what, err := d.EnsureHTTPS(model.HttpsRecordData{Name: "play.example.com",
		Target: "node.example.com", Port: 51471, Priority: 1, TTL: 60})
	if err != nil {
		t.Fatal(err)
	}
	if what != "" {
		t.Errorf("reported a change that is not one: %q", what)
	}
	for _, c := range calls() {
		if c.Method == "PATCH" {
			t.Error("wrote an unchanged record")
		}
	}
}

func TestAMultiRecordRRsetIsNeverDeleted(t *testing.T) {
	d, calls := server(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(rrset{TTL: 60, Records: []string{
			"0 5 51471 node.example.com.", "10 0 25565 backup.example.com."}})
	})
	_, err := d.Delete("SRV", "_mc._tcp.example.com", "node.example.com")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "2 records") {
		t.Errorf("message = %v", err)
	}
	for _, c := range calls() {
		if c.Method == "DELETE" {
			t.Fatal("issued a DELETE that would destroy the user's record")
		}
	}
}

func TestTheWitnessChecksTheTargetNotAnyField(t *testing.T) {
	d, calls := server(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(rrset{TTL: 60,
			Records: []string{"0 5 51471 somewhere-else.net."}})
	})
	if _, err := d.Delete("SRV", "_mc._tcp.example.com", "0"); err == nil {
		t.Error("a priority field must not authorise a delete")
	}
	for _, c := range calls() {
		if c.Method == "DELETE" {
			t.Fatal("issued a DELETE on a record that is not ours")
		}
	}
}

func TestDeleteWithoutAWitnessIsRefused(t *testing.T) {
	d, _ := server(t, func(w http.ResponseWriter, r *http.Request) {})
	_, err := d.Delete("SRV", "_mc._tcp.example.com", "")
	if err == nil || !strings.Contains(err.Error(), "witness") {
		t.Errorf("err = %v", err)
	}
}

func Test429BecomesRateLimitedWithRetryAfter(t *testing.T) {
	d, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		w.Write([]byte(`{"detail":"throttled"}`))
	})
	_, err := d.call("GET", d.url("domains", "example.com"), nil)
	rl, ok := err.(*RateLimited)
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
		d, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
			w.Write([]byte(`{"detail":"x"}`))
		})
		_, err := d.call("GET", d.url("domains", "example.com"), nil)
		_, isRL := err.(*RateLimited)
		if isRL != c.retryable {
			t.Errorf("HTTP %d: retryable=%v, want %v", c.code, isRL, c.retryable)
		}
	}
}

func TestAMissingMinimumTtlIsRefusedRatherThanGuessed(t *testing.T) {
	d, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"example.com"}`))
	})
	d.minTTL = 0
	_, err := d.MinimumTTL()
	if err == nil || !strings.Contains(err.Error(), "Refusing to guess") {
		t.Errorf("err = %v", err)
	}
}

func TestTtlBelowTheFloorIsRefusedWithTheNumberAndTheWayOut(t *testing.T) {
	d, _ := server(t, func(w http.ResponseWriter, r *http.Request) {})
	d.minTTL, d.minTTLAt = 3600, time.Now()
	_, err := d.EnsureA("node.example.com", "1.2.3.4", 60)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "3600") ||
		!strings.Contains(err.Error(), "exception") {
		t.Errorf("message must name the floor and the way out: %v", err)
	}
}

func TestNameOutsideTheDomainIsRefused(t *testing.T) {
	d, _ := server(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, err := d.EnsureA("node.elsewhere.org", "1.2.3.4", 60); err == nil {
		t.Error("expected a refusal")
	}
}

func TestUrlEscapingKeepsApexAndWildcardButNeutralisesTheRest(t *testing.T) {
	d := &DesecProvider{Domain: "example.com", baseURL: "http://x"}
	if got := d.url("domains", "example.com", "rrsets", "@", "A"); !strings.Contains(got, "/@/") {
		t.Errorf("apex must stay literal: %s", got)
	}
	if got := d.url("rrsets", "*"); !strings.Contains(got, "/*/") {
		t.Errorf("wildcard must stay literal: %s", got)
	}
	if got := d.url("rrsets", "a?x=1#y"); strings.Contains(got, "?") ||
		strings.Contains(got, "#") {
		t.Errorf("path-rewriting characters must be escaped: %s", got)
	}
	if got := d.url("rrsets", "_minecraft._tcp.play"); !strings.Contains(
		got, "_minecraft._tcp.play") {
		t.Errorf("ordinary SRV labels must survive: %s", got)
	}
}
