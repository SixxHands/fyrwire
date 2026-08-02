package preset

import (
	"testing"
)

func TestEveryServiceLabelIsWellFormed(t *testing.T) {
	for _, p := range All {
		if p.Service == "" {
			continue
		}
		if err := ValidateService(p.Service); err != nil {
			t.Errorf("%s: %v", p.Key, err)
		}
	}
}

func TestServiceValidationRejectsWhatRFC6335Forbids(t *testing.T) {
	for _, bad := range []string{
		"", "-lead", "trail-", "two--hyphens", "1234", "way-too-long-a-label",
		"has space", "under_score",
	} {
		if err := ValidateService(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	for _, good := range []string{"minecraft", "http", "a", "x-1", "sip-tls"} {
		if err := ValidateService(good); err != nil {
			t.Errorf("%q is legal: %v", good, err)
		}
	}
}

func TestFollowsIsAlwaysOneOfTheThreeKnownValues(t *testing.T) {
	for _, p := range All {
		switch p.Follows {
		case "srv", "srv-setup", "manual":
		default:
			t.Errorf("%s: follows = %q", p.Key, p.Follows)
		}
	}
}

func TestPresetsThatDoNotFollowSrvSayWhy(t *testing.T) {

	for _, p := range All {
		if p.Follows != "srv" && p.Note == "" {
			t.Errorf("%s claims follows=%q but offers no explanation",
				p.Key, p.Follows)
		}
	}
}

func TestEveryPresetIsInAKnownGroup(t *testing.T) {
	known := map[string]bool{}
	for _, g := range Groups {
		known[g] = true
	}
	for _, p := range All {
		if !known[p.Group] {
			t.Errorf("%s is in unknown group %q", p.Key, p.Group)
		}
	}
}

func TestProtocolAndRecordAreValid(t *testing.T) {
	for _, p := range All {
		if p.Protocol != "tcp" && p.Protocol != "udp" {
			t.Errorf("%s: protocol %q", p.Key, p.Protocol)
		}
		switch p.Record {
		case "srv", "https", "none":
		default:
			t.Errorf("%s: record %q", p.Key, p.Record)
		}
	}
}

func TestLookupWorks(t *testing.T) {
	if _, ok := Get("minecraft"); !ok {
		t.Error("minecraft must be present")
	}
	if _, ok := Get("no-such-preset"); ok {
		t.Error("unknown keys must not resolve")
	}
}
