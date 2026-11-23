package preset

import "testing"

func Test_every_service_label_is_well_formed(t *testing.T) {
	for _, p := range All {
		if p.Service == "" {
			continue
		}
		if err := CheckServiceLabel(p.Service); err != nil {
			t.Errorf("%s: %v", p.Key, err)
		}
	}
}

func Test_service_validation_rejects_invalid_labels(t *testing.T) {
	for _, bad := range []string{
		"", "-lead", "trail-", "two--hyphens", "1234", "way-too-long-a-label",
		"has space", "under_score",
	} {
		if err := CheckServiceLabel(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	for _, good := range []string{"minecraft", "http", "a", "x-1", "sip-tls"} {
		if err := CheckServiceLabel(good); err != nil {
			t.Errorf("%q is legal: %v", good, err)
		}
	}
}

func Test_follows_is_valid(t *testing.T) {
	for _, p := range All {
		switch p.Follows {
		case "srv", "srv-setup", "manual":
		default:
			t.Errorf("%s: follows = %q", p.Key, p.Follows)
		}
	}
}

func Test_non_srv_presets_have_notes(t *testing.T) {
	for _, p := range All {
		if p.Follows != "srv" && p.Note == "" {
			t.Errorf("%s claims follows=%q without a note", p.Key, p.Follows)
		}
	}
}

func Test_every_preset_is_in_a_known_group(t *testing.T) {
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

func Test_protocol_and_record_are_valid(t *testing.T) {
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

func Test_lookup_works(t *testing.T) {
	if _, ok := Get("minecraft"); !ok {
		t.Error("minecraft must be present")
	}
	if _, ok := Get("no-such-preset"); ok {
		t.Error("unknown keys must not match")
	}
}
