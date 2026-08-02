package provider

import (
	"strings"
	"testing"
)

func TestTargetOfKnowsWhereTheHostnameLives(t *testing.T) {
	for _, c := range []struct{ rtype, value, want string }{
		{"SRV", "0 5 51471 node.example.com.", "node.example.com."},
		{"HTTPS", "1 node.example.com. port=51471", "node.example.com."},
		{"A", "203.0.113.5", "203.0.113.5"},
		{"SRV", "0 5", ""},
		{"TXT", "whatever", ""},
	} {
		if got := TargetOf(c.rtype, c.value); got != c.want {
			t.Errorf("%s %q -> %q, want %q", c.rtype, c.value, got, c.want)
		}
	}
}

func TestAnIpLiteralTargetIsRefusedWithTheReason(t *testing.T) {
	err := CheckTarget("203.0.113.5")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Errorf("message = %v", err)
	}
}

func TestAnUnderscoreInTheTargetIsRefused(t *testing.T) {

	if err := CheckTarget("node_1.example.com"); err == nil {
		t.Error("expected a refusal")
	}
	if err := CheckTarget("node.example.com"); err != nil {
		t.Errorf("an ordinary name must pass: %v", err)
	}
}

func TestCanonMakesEqualInMeaningCompareEqual(t *testing.T) {
	a := `1 node.example.com. port=51471 alpn=h2`
	b := `1 node.example.com. alpn="h2" port="51471"`
	if a == b {
		t.Fatal("the test is pointless if these are byte-equal")
	}
	if Canon("HTTPS", a) != Canon("HTTPS", b) {
		t.Errorf("reordered and quoted params must compare equal:\n  %q\n  %q",
			Canon("HTTPS", a), Canon("HTTPS", b))
	}

	if Canon("SRV", "0 5 1 Node.Example.COM.") != Canon("SRV", "0 5 1 node.example.com.") {
		t.Error("SRV target case must not count as a change")
	}

	if Canon("SRV", "0 5 1 a.example.com.") == Canon("SRV", "0 5 2 a.example.com.") {
		t.Error("a different port IS a change")
	}
}

func TestErrorBodiesAreFlattenedIntoASentence(t *testing.T) {
	got := jsonOr([]byte(`{"ttl":["Ensure this value is >= 3600."]}`), "x")
	if !strings.Contains(got, "Ensure this value") || !strings.Contains(got, "ttl") {
		t.Errorf("got %q", got)
	}
	if got := jsonOr([]byte(`{"detail":"Invalid token."}`), "x"); got != "Invalid token." {
		t.Errorf("got %q", got)
	}
	if got := jsonOr([]byte("not json"), "fallback"); got != "not json" {
		t.Errorf("got %q", got)
	}
}

func TestBothProvidersAreRegistered(t *testing.T) {
	have := map[string]bool{}
	for _, n := range Available() {
		have[n] = true
	}
	if !have["cloudflare"] || !have["desec"] {
		t.Errorf("registered: %v", Available())
	}
}
