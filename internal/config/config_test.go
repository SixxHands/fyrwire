package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sixxhands/fyrwire/internal/config"

	_ "github.com/sixxhands/fyrwire/internal/provider"
	_ "github.com/sixxhands/fyrwire/internal/source"
)

func corpusDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "testdata", "config")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("corpus missing at %s: %v", dir, err)
	}
	return dir
}

func TestSharedConfigCorpus(t *testing.T) {
	dir := corpusDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) < 10 {
		t.Fatalf("corpus has only %d cases", len(entries))
	}
	t.Setenv("FYRWIRE_CF_TOKEN", "test-token")

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			expect := "accept"
			var contains []string
			for _, line := range strings.Split(text, "\n") {
				switch {
				case strings.HasPrefix(line, "#EXPECT"):
					expect = strings.TrimSpace(strings.TrimPrefix(line, "#EXPECT"))
				case strings.HasPrefix(line, "#CONTAINS"):
					contains = append(contains,
						strings.TrimSpace(strings.TrimPrefix(line, "#CONTAINS")))
				}
			}
			_, err = config.Parse(text)
			got := "accept"
			msg := ""
			if err != nil {
				got, msg = "reject", err.Error()
			}
			if got != expect {
				t.Fatalf("expected %s, got %s (%s)", expect, got, msg)
			}
			for _, c := range contains {
				if !strings.Contains(strings.ToLower(msg), strings.ToLower(c)) {
					t.Errorf("message must mention %q, got %q", c, msg)
				}
			}
		})
	}
}
