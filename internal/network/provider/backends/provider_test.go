package backends

import (
	"strings"
	"testing"

	"github.com/sixxhands/fyrwire/internal/network/provider"
)

func Test_target_of_knows_where_the_hostname_lives(t *testing.T) {
	for _, c := range []struct{ recordType, value, want string }{
		{"SRV", "0 5 51471 node.example.com.", "node.example.com."},
		{"HTTPS", "1 node.example.com. port=51471", "node.example.com."},
		{"A", "203.0.113.5", "203.0.113.5"},
		{"SRV", "0 5", ""},
		{"TXT", "whatever", ""},
	} {
		if got := provider.TargetOf(c.recordType, c.value); got != c.want {
			t.Errorf("%s %q -> %q, want %q", c.recordType, c.value, got, c.want)
		}
	}
}

func Test_ip_literal_target_is_refused_with_the_reason(t *testing.T) {
	err := provider.CheckTarget("203.0.113.5")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Errorf("message = %v", err)
	}
}

func Test_underscore_in_target_is_refused(t *testing.T) {
	if err := provider.CheckTarget("node_1.example.com"); err == nil {
		t.Error("expected a refusal")
	}
	if err := provider.CheckTarget("node.example.com"); err != nil {
		t.Errorf("an ordinary name must pass: %v", err)
	}
}

func Test_standard_value_compares_equivalent_records_equal(t *testing.T) {
	a := `1 node.example.com. port=51471 alpn=h2`
	b := `1 node.example.com. alpn="h2" port="51471"`
	if provider.StandardValue("HTTPS", a) != provider.StandardValue("HTTPS", b) {
		t.Errorf("reordered and quoted params must compare equal:\n  %q\n  %q",
			provider.StandardValue("HTTPS", a), provider.StandardValue("HTTPS", b))
	}
	if provider.StandardValue("SRV", "0 5 1 Node.Example.COM.") != provider.StandardValue("SRV", "0 5 1 node.example.com.") {
		t.Error("SRV target case must not count as a change")
	}
	if provider.StandardValue("SRV", "0 5 1 a.example.com.") == provider.StandardValue("SRV", "0 5 2 a.example.com.") {
		t.Error("different port must count as a change")
	}
}

func Test_error_bodies_are_flattened(t *testing.T) {
	got := provider.JsonOr([]byte(`{"ttl":["Ensure this value is >= 3600."]}`), "x")
	if !strings.Contains(got, "Ensure this value") || !strings.Contains(got, "ttl") {
		t.Errorf("got %q", got)
	}
	if got := provider.JsonOr([]byte(`{"detail":"Invalid token."}`), "x"); got != "Invalid token." {
		t.Errorf("got %q", got)
	}
	if got := provider.JsonOr([]byte("not json"), "fallback"); got != "not json" {
		t.Errorf("got %q", got)
	}
}

func Test_both_providers_are_registered(t *testing.T) {
	have := map[string]bool{}
	for _, n := range provider.Available() {
		have[n] = true
	}
	if !have["cloudflare"] || !have["desec"] {
		t.Errorf("registered: %v", provider.Available())
	}
}
