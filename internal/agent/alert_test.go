package agent

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sixxhands/fyrwire/internal/model"
	"github.com/sixxhands/fyrwire/internal/notify"
)

func TestAlertsActuallyReachTheNotifier(t *testing.T) {
	var mu sync.Mutex
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits++
			mu.Unlock()
		}))
	defer srv.Close()

	src := &fakeSource{err: model.Errorf("gateway unreachable")}
	a := New(src, &fakeProvider{}, plan(), filepath.Join(t.TempDir(), "s.json"),
		OptionsData{
			Notifier: &notify.Notifier{Webhook: srv.URL, AfterFailures: 2,
				Log: func(string) {}},
			NodeName: "node.example.com",
		})
	a.Tick(false)
	mu.Lock()
	after1 := hits
	mu.Unlock()
	if after1 != 0 {
		t.Errorf("one failure must not alert, sent %d", after1)
	}
	a.Tick(false)
	mu.Lock()
	after2 := hits
	mu.Unlock()
	if after2 != 1 {
		t.Fatalf("the second failure must alert, sent %d", after2)
	}

	src.err = nil
	src.res = res("1.2.3.4", map[string]int{"mc": 51471})
	a.Tick(false)
	mu.Lock()
	after3 := hits
	mu.Unlock()
	if after3 != 2 {
		t.Errorf("recovery must alert immediately, sent %d", after3)
	}
	if a.State.AlertedDown {
		t.Error("the alert state must clear on recovery")
	}
}

func TestAlertStateSurvivesARestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "s.json")
	var mu sync.Mutex
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits++
			mu.Unlock()
		}))
	defer srv.Close()
	mk := func() *Agent {
		return New(&fakeSource{err: model.Errorf("down")}, &fakeProvider{},
			plan(), statePath, OptionsData{
				Notifier: &notify.Notifier{Webhook: srv.URL, AfterFailures: 1,
					Log: func(string) {}},
				NodeName: "n"})
	}
	mk().Tick(false)
	mu.Lock()
	first := hits
	mu.Unlock()
	if first != 1 {
		t.Fatalf("sent %d", first)
	}
	mk().Tick(false)
	mu.Lock()
	second := hits
	mu.Unlock()
	if second != 1 {
		t.Errorf("a restart re-announced the same episode (%d alerts)", second)
	}
}

func TestNoNotifierIsSafe(t *testing.T) {
	a := New(&fakeSource{err: model.Errorf("down")}, &fakeProvider{}, plan(),
		filepath.Join(t.TempDir(), "s.json"), OptionsData{})
	if r := a.Tick(false); r.OK {
		t.Error("expected the failure to be reported")
	}
}
