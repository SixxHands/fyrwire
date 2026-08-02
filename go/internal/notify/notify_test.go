package notify

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func recorder(t *testing.T) (*Notifier, func() int) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			count++
			mu.Unlock()
		}))
	t.Cleanup(srv.Close)
	return &Notifier{Webhook: srv.URL, AfterFailures: 2}, func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

func TestFailuresAreDebounced(t *testing.T) {
	n, sent := recorder(t)
	st := &AlertState{}
	n.Evaluate(AlertStatusData{OK: false, Failures: 1, Error: "x"}, st, "node")
	if sent() != 0 {
		t.Error("one failure must not alert")
	}
	n.Evaluate(AlertStatusData{OK: false, Failures: 2, Error: "x"}, st, "node")
	if sent() != 1 {
		t.Errorf("the second failure must alert, sent %d", sent())
	}
}

func TestAlertsFireOnTransitionsNotOnState(t *testing.T) {
	n, sent := recorder(t)
	st := &AlertState{}
	for i := 2; i < 10; i++ {
		n.Evaluate(AlertStatusData{OK: false, Failures: i, Error: "x"}, st, "node")
	}
	if sent() != 1 {
		t.Errorf("eight failing cycles sent %d alerts, want 1", sent())
	}
}

func TestRecoveryIsNotDebounced(t *testing.T) {
	n, sent := recorder(t)
	st := &AlertState{}
	n.Evaluate(AlertStatusData{OK: false, Failures: 2, Error: "x"}, st, "node")
	n.Evaluate(AlertStatusData{OK: true, Host: "1.2.3.4"}, st, "node")
	if sent() != 2 {
		t.Errorf("recovery must fire immediately, sent %d", sent())
	}
	if st.Down {
		t.Error("state must clear on recovery")
	}
}

func TestAPerServiceOutageIsReportedOnceEachWay(t *testing.T) {
	n, sent := recorder(t)
	st := &AlertState{}
	n.Evaluate(AlertStatusData{OK: true, Host: "h", Skipped: []string{"vh"}}, st, "node")
	if sent() != 1 {
		t.Fatalf("service-down alert missing, sent %d", sent())
	}
	n.Evaluate(AlertStatusData{OK: true, Host: "h", Skipped: []string{"vh"}}, st, "node")
	if sent() != 1 {
		t.Errorf("still-down is not news, sent %d", sent())
	}
	n.Evaluate(AlertStatusData{OK: true, Host: "h"}, st, "node")
	if sent() != 2 {
		t.Errorf("service-up alert missing, sent %d", sent())
	}
}

func TestABrokenWebhookDoesNotPropagate(t *testing.T) {
	n := &Notifier{Webhook: "http://127.0.0.1:1", AfterFailures: 1,
		Log: func(string) {}}
	st := &AlertState{}

	n.Evaluate(AlertStatusData{OK: false, Failures: 1, Error: "x"}, st, "node")
	if !st.Down {
		t.Error("state must advance even when delivery fails")
	}
}
