package daemon_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sixxhands/fyrwire/internal/daemon/notify"
	"github.com/sixxhands/fyrwire/internal/daemon/notify/channels"
)

func testRecorder(t *testing.T) (*notify.Notifier, func() int) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)

	blueprint, err := notify.CreateNotifierBlueprint(srv.URL, "", 2, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	return notify.NotifierFactory{}.Create(blueprint), func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

func TestFailuresAreDebounced(t *testing.T) {
	notifier, sent := testRecorder(t)
	state := &notify.AlertState{}
	notifier.CheckTransitions(notify.AlertStatusData{Ok: false, Failures: 1, Error: "x"}, state, "node")
	if sent() != 0 {
		t.Error("one failure must not alert")
	}
	notifier.CheckTransitions(notify.AlertStatusData{Ok: false, Failures: 2, Error: "x"}, state, "node")
	if sent() != 1 {
		t.Errorf("second failure must alert, sent %d", sent())
	}
}

func TestAlertsFireOnTransitionsOnly(t *testing.T) {
	notifier, sent := testRecorder(t)
	state := &notify.AlertState{}
	for count := 2; count < 10; count++ {
		notifier.CheckTransitions(notify.AlertStatusData{Ok: false, Failures: count, Error: "x"}, state, "node")
	}
	if sent() != 1 {
		t.Errorf("eight failing cycles sent %d alerts, want 1", sent())
	}
}

func TestRecoveryIsImmediate(t *testing.T) {
	notifier, sent := testRecorder(t)
	state := &notify.AlertState{}
	notifier.CheckTransitions(notify.AlertStatusData{Ok: false, Failures: 2, Error: "x"}, state, "node")
	notifier.CheckTransitions(notify.AlertStatusData{Ok: true, Host: "1.2.3.4"}, state, "node")
	if sent() != 2 {
		t.Errorf("recovery must fire immediately, sent %d", sent())
	}
	if state.Down {
		t.Error("state must clear on recovery")
	}
}

func TestPerServiceOutageReportedOnceEachWay(t *testing.T) {
	notifier, sent := testRecorder(t)
	state := &notify.AlertState{}
	notifier.CheckTransitions(notify.AlertStatusData{Ok: true, Host: "h", Skipped: []string{"vh"}}, state, "node")
	if sent() != 1 {
		t.Fatalf("service-down alert missing, sent %d", sent())
	}
	notifier.CheckTransitions(notify.AlertStatusData{Ok: true, Host: "h", Skipped: []string{"vh"}}, state, "node")
	if sent() != 1 {
		t.Errorf("repeated outage should not re-alert, sent %d", sent())
	}
	notifier.CheckTransitions(notify.AlertStatusData{Ok: true, Host: "h"}, state, "node")
	if sent() != 2 {
		t.Errorf("service-up alert missing, sent %d", sent())
	}
}

func TestBrokenWebhookDoesNotStopStateAdvance(t *testing.T) {
	blueprint, err := notify.CreateNotifierBlueprint("http://127.0.0.1:1", "", 1, time.Second, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	notifier := notify.NotifierFactory{}.Create(blueprint)
	state := &notify.AlertState{}
	notifier.CheckTransitions(notify.AlertStatusData{Ok: false, Failures: 1, Error: "x"}, state, "node")
	if !state.Down {
		t.Error("state must advance even when delivery fails")
	}
}

type recordedAlert struct {
	title string
	body  string
	color int
}

type memoryChannel struct {
	alerts []recordedAlert
}

func (channel *memoryChannel) Send(title, body string, color int) error {
	channel.alerts = append(channel.alerts, recordedAlert{title: title, body: body, color: color})
	return nil
}

func TestCustomAlertChannelStrategyReceivesTransitions(t *testing.T) {
	channel := &memoryChannel{}
	blueprint, err := notify.CreateNotifierBlueprint("", "", 1, 0, nil, channel)
	if err != nil {
		t.Fatal(err)
	}
	notifier := notify.NotifierFactory{}.Create(blueprint)
	state := &notify.AlertState{}

	notifier.CheckTransitions(notify.AlertStatusData{Ok: false, Failures: 1, Error: "offline"}, state, "node")
	notifier.CheckTransitions(notify.AlertStatusData{Ok: true, Host: "1.2.3.4"}, state, "node")

	if len(channel.alerts) != 2 {
		t.Fatalf("custom channel got %d alerts, want 2", len(channel.alerts))
	}
	if channel.alerts[0].title != "node: cannot reach the gateway" {
		t.Errorf("first alert title = %q", channel.alerts[0].title)
	}
	if channel.alerts[1].title != "node: recovered" {
		t.Errorf("second alert title = %q", channel.alerts[1].title)
	}
}

func TestWebhookChannelSelectsFormatterByUrl(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		expectKey string
	}{
		{"discord", "https://discord.com/api/webhooks/1/tok", "embeds"},
		{"slack", "https://hooks.slack.com/services/a/b/c", "text"},
		{"generic", "https://alerts.example.com/hook", "title"},
	}

	for _, testCase := range cases {
		channel := channels.CreateWebhookChannel(testCase.url, time.Second)
		formatted := channel.Formatter.Format("node: recovered", "Back online", 0x6EDC96)
		raw, err := json.Marshal(formatted)
		if err != nil {
			t.Fatalf("%s marshal failed: %v", testCase.name, err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("%s unmarshal failed: %v", testCase.name, err)
		}
		if _, ok := body[testCase.expectKey]; !ok {
			t.Errorf("%s body %s missing %q", testCase.name, string(raw), testCase.expectKey)
		}
	}
}
