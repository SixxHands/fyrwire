package daemon_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sixxhands/fyrwire/internal/daemon/agent"
	"github.com/sixxhands/fyrwire/internal/daemon/notify"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type alertObserverTest struct {
	notifier *notify.Notifier
	nodeName string
}

func (observer alertObserverTest) OnTick(result agent.TickResult, state *agent.AgentState) {
	alertState := &notify.AlertState{
		Down:     state.AlertedDown,
		Services: state.AlertedServices,
	}
	errorText := ""
	if result.Err != nil {
		errorText = result.Err.Error()
	}
	status := notify.AlertStatusData{
		Ok:       result.Ok,
		Host:     result.Host,
		Error:    errorText,
		Skipped:  result.Skipped,
		Reasons:  result.Reasons,
		Failures: state.ConsecutiveFailures,
		KeptPort: state.LastPorts,
	}
	observer.notifier.CheckTransitions(status, alertState, observer.nodeName)
	state.AlertedDown = alertState.Down
	state.AlertedServices = alertState.Services
}

func newAlertServer(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return hits
	}
}

func buildAlertObserver(t *testing.T, webhook string, afterFailures int, nodeName string) agent.IAgentObserver {
	t.Helper()
	blueprint, err := notify.CreateNotifierBlueprint(webhook, "", afterFailures, 5*time.Second, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	return alertObserverTest{
		notifier: notify.NotifierFactory{}.Create(blueprint),
		nodeName: nodeName,
	}
}

func Test_alerts_reach_notifier_through_observer(t *testing.T) {
	srv, hits := newAlertServer(t)
	src := &fakeSourceTest{err: model.Errorf("gateway unreachable")}
	observer := buildAlertObserver(t, srv.URL, 2, "node.example.com")
	runner := buildTestAgent(t, src, &fakeProviderTest{}, testPlan(), filepath.Join(t.TempDir(), "s.json"),
		agent.OptionsData{Observers: []agent.IAgentObserver{observer}, NodeName: "node.example.com"})

	runner.Tick(false)
	if got := hits(); got != 0 {
		t.Errorf("one failure must not alert, sent %d", got)
	}
	runner.Tick(false)
	if got := hits(); got != 1 {
		t.Fatalf("second failure must alert, sent %d", got)
	}

	src.err = nil
	src.data = endpoints("1.2.3.4", map[string]int{"mc": 51471})
	runner.Tick(false)
	if got := hits(); got != 2 {
		t.Errorf("recovery must alert immediately, sent %d", got)
	}
	if runner.State.AlertedDown {
		t.Error("alert state must clear on recovery")
	}
}

func Test_alert_state_survives_restart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "s.json")
	srv, hits := newAlertServer(t)
	makeRunner := func() *agent.Agent {
		observer := buildAlertObserver(t, srv.URL, 1, "n")
		return buildTestAgent(t, &fakeSourceTest{err: model.Errorf("down")}, &fakeProviderTest{}, testPlan(), statePath,
			agent.OptionsData{Observers: []agent.IAgentObserver{observer}, NodeName: "n"})
	}
	makeRunner().Tick(false)
	makeRunner().Tick(false)
	if total := hits(); total != 1 {
		t.Errorf("restart re-announced same outage (%d alerts)", total)
	}
}

type countingObserverTest struct {
	seen []bool
}

func (observer *countingObserverTest) OnTick(result agent.TickResult, _ *agent.AgentState) {
	observer.seen = append(observer.seen, result.Ok)
}

func Test_multiple_observers_receive_every_tick(t *testing.T) {
	first := &countingObserverTest{}
	second := &countingObserverTest{}
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": 51471})}
	runner := buildTestAgent(t, src, &fakeProviderTest{}, testPlan(), filepath.Join(t.TempDir(), "s.json"),
		agent.OptionsData{Observers: []agent.IAgentObserver{first, second}})

	runner.Tick(false)
	src.err = model.Errorf("gateway lost")
	runner.Tick(false)

	if len(first.seen) != 2 || len(second.seen) != 2 {
		t.Fatalf("observer counts = %d, %d, want 2 each", len(first.seen), len(second.seen))
	}
	if !first.seen[0] || first.seen[1] {
		t.Errorf("unexpected tick statuses: %v", first.seen)
	}
}

func Test_no_observers_is_safe(t *testing.T) {
	runner := buildTestAgent(t, &fakeSourceTest{err: model.Errorf("down")}, &fakeProviderTest{}, testPlan(),
		filepath.Join(t.TempDir(), "s.json"), agent.OptionsData{})
	if result := runner.Tick(false); result.Ok {
		t.Error("expected failure report")
	}
}
