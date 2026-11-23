package daemon_test

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sixxhands/fyrwire/internal/daemon/agent"
	"github.com/sixxhands/fyrwire/internal/network/provider"
	"github.com/sixxhands/fyrwire/internal/network/relay"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type fakeSourceTest struct {
	data model.EndpointData
	err  error
	hits int
}

func (source *fakeSourceTest) Name() string { return "fake" }
func (source *fakeSourceTest) FindEndpoints([]model.PortRequestData) (model.EndpointData, error) {
	source.hits++
	return source.data, source.err
}

type fakeProviderTest struct {
	aCalls   []string
	srvCalls []model.SrvRecordData
	err      error
}

func (dns *fakeProviderTest) Name() string { return "fake" }
func (dns *fakeProviderTest) EnsureA(name, ip string, ttl int) (string, error) {
	if dns.err != nil {
		return "", dns.err
	}
	dns.aCalls = append(dns.aCalls, name+"="+ip)
	return "set A " + name, nil
}
func (dns *fakeProviderTest) EnsureSrv(record model.SrvRecordData) (string, error) {
	if dns.err != nil {
		return "", dns.err
	}
	dns.srvCalls = append(dns.srvCalls, record)
	return "set SRV " + record.Name, nil
}
func (dns *fakeProviderTest) EnsureHttps(record model.HttpsRecordData) (string, error) {
	if dns.err != nil {
		return "", dns.err
	}
	return "set HTTPS " + record.Name, nil
}
func (dns *fakeProviderTest) Remove(recordType, name, expect string) (string, error) {
	return "", nil
}

func testPlan() model.NodePlanData {
	return model.NodePlanData{
		AName: "node.example.com",
		Ttl:   60,
		Services: []model.ServiceData{{
			Key:          "mc",
			InternalPort: 25565,
			SrvName:      "_minecraft._tcp.example.com",
			Protocol:     "tcp",
			Weight:       5,
			Record:       model.RecordSrv,
		}},
	}
}

func buildTestAgent(t *testing.T, src *fakeSourceTest, prov *fakeProviderTest, plan model.NodePlanData, statePath string, options agent.OptionsData) *agent.Agent {
	t.Helper()
	blueprint, err := agent.CreateAgentBlueprint(src, prov, plan, statePath, options)
	if err != nil {
		t.Fatal(err)
	}
	return agent.AgentFactory{}.Create(blueprint)
}

func newTestAgent(t *testing.T, src *fakeSourceTest, prov *fakeProviderTest) *agent.Agent {
	t.Helper()
	return buildTestAgent(t, src, prov, testPlan(), filepath.Join(t.TempDir(), "state.json"), agent.OptionsData{})
}

func endpoints(host string, ports map[string]int) model.EndpointData {
	return model.EndpointData{Host: host, Ports: ports, Partial: map[string]bool{}}
}

func Test_publishes_only_on_change(t *testing.T) {
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": 51471})}
	prov := &fakeProviderTest{}
	runner := newTestAgent(t, src, prov)

	first := runner.Tick(false)
	if len(first.Changes) == 0 {
		t.Fatal("first tick must publish")
	}
	second := runner.Tick(false)
	if len(second.Changes) != 0 {
		t.Errorf("unexpected changes: %v", second.Changes)
	}
	if len(prov.aCalls) != 1 || len(prov.srvCalls) != 1 {
		t.Errorf("A=%d SRV=%d, want 1 each", len(prov.aCalls), len(prov.srvCalls))
	}
}

func Test_rotated_port_is_published(t *testing.T) {
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": 51471})}
	prov := &fakeProviderTest{}
	runner := newTestAgent(t, src, prov)
	runner.Tick(false)
	src.data = endpoints("1.2.3.4", map[string]int{"mc": 40000})
	runner.Tick(false)
	if len(prov.srvCalls) != 2 || prov.srvCalls[1].Port != 40000 {
		t.Fatalf("SRV calls = %+v", prov.srvCalls)
	}
	if len(prov.aCalls) != 1 {
		t.Errorf("A rewritten %d times for unchanged address", len(prov.aCalls))
	}
}

func Test_address_change_leaves_srv_alone(t *testing.T) {
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": 51471})}
	prov := &fakeProviderTest{}
	runner := newTestAgent(t, src, prov)
	runner.Tick(false)
	src.data = endpoints("5.6.7.8", map[string]int{"mc": 51471})
	runner.Tick(false)
	if len(prov.aCalls) != 2 {
		t.Errorf("A must be republished: %v", prov.aCalls)
	}
	if len(prov.srvCalls) != 1 {
		t.Errorf("SRV must not be rewritten: %d", len(prov.srvCalls))
	}
}

func Test_failure_never_clears_dns(t *testing.T) {
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": 51471})}
	prov := &fakeProviderTest{}
	runner := newTestAgent(t, src, prov)
	runner.Tick(false)
	before := len(prov.aCalls) + len(prov.srvCalls)

	src.err = model.Errorf("gateway unreachable")
	result := runner.Tick(false)
	if result.Ok {
		t.Fatal("expected failure")
	}
	if after := len(prov.aCalls) + len(prov.srvCalls); after != before {
		t.Errorf("failing cycle touched DNS: %d -> %d", before, after)
	}
	if runner.State.LastHost != "1.2.3.4" {
		t.Errorf("last host lost: %q", runner.State.LastHost)
	}
}

func Test_partial_service_does_not_block_others(t *testing.T) {
	plan := testPlan()
	plan.Services = append(plan.Services, model.ServiceData{
		Key: "vh", InternalPort: 2456,
		SrvName: "_valheim._udp.example.com", Protocol: "udp", Weight: 5,
		Record: model.RecordSrv,
	})
	src := &fakeSourceTest{data: model.EndpointData{
		Host: "1.2.3.4", Ports: map[string]int{"mc": 51471}, Partial: map[string]bool{"vh": true},
	}}
	prov := &fakeProviderTest{}
	runner := buildTestAgent(t, src, prov, plan, filepath.Join(t.TempDir(), "s.json"), agent.OptionsData{})
	result := runner.Tick(false)
	if !result.Ok {
		t.Fatalf("partial result is not a node failure: %v", result.Err)
	}
	if len(prov.srvCalls) != 1 || prov.srvCalls[0].Name != "_minecraft._tcp.example.com" {
		t.Errorf("working service must publish: %+v", prov.srvCalls)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "vh" {
		t.Errorf("skipped = %v", result.Skipped)
	}
}

func Test_state_survives_restart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": 51471})}
	buildTestAgent(t, src, &fakeProviderTest{}, testPlan(), statePath, agent.OptionsData{}).Tick(false)

	restarted := buildTestAgent(t, src, &fakeProviderTest{}, testPlan(), statePath, agent.OptionsData{})
	if result := restarted.Tick(false); len(result.Changes) != 0 {
		t.Errorf("restart republished unnecessarily: %v", result.Changes)
	}
}

func Test_corrupt_state_file_is_not_fatal(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": 51471})}
	runner := buildTestAgent(t, src, &fakeProviderTest{}, testPlan(), statePath, agent.OptionsData{})
	if result := runner.Tick(false); !result.Ok {
		t.Errorf("corrupt state stopped agent: %v", result.Err)
	}
}

func Test_udp_is_not_relayed_unless_enabled(t *testing.T) {
	plan := testPlan()
	plan.Services[0].Protocol = "udp"
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": 51471})}
	runner := buildTestAgent(t, src, &fakeProviderTest{}, plan, filepath.Join(t.TempDir(), "s.json"),
		agent.OptionsData{Relay: true, RelayUdp: false, BindHost: "127.0.0.1", Udp: relay.UdpOptionsData{}})
	defer runner.Stop()
	runner.Tick(false)
	if _, present := runner.Relays().Stats()["mc"]; present {
		t.Error("udp service must not be relayed with relay_udp off")
	}
}

func Test_loop_survives_rate_limit(t *testing.T) {
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": 51471})}
	prov := &fakeProviderTest{err: &provider.RateLimited{Msg: "slow down"}}
	runner := newTestAgent(t, src, prov)
	done := make(chan struct{})
	go func() {
		runner.Run(time.Millisecond, 0, 3)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run stalled on rate limit")
	}
	if src.hits < 3 {
		t.Errorf("loop stopped early after %d cycles", src.hits)
	}
}

func Test_rate_limit_backs_off_more_than_generic_error(t *testing.T) {
	elapsedFor := func(err error) time.Duration {
		src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": 51471})}
		runner := newTestAgent(t, src, &fakeProviderTest{err: err})
		start := time.Now()
		runner.Run(80*time.Millisecond, 0, 3)
		return time.Since(start)
	}
	generic := elapsedFor(model.Errorf("bad token"))
	limited := elapsedFor(&provider.RateLimited{Msg: "slow down"})
	if limited <= generic {
		t.Errorf("rate limit must back off: limited=%v generic=%v", limited, generic)
	}
}

func Test_retry_after_is_respected(t *testing.T) {
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": 51471})}
	runner := newTestAgent(t, src, &fakeProviderTest{
		err: &provider.RateLimited{Msg: "slow", RetryAfter: 400 * time.Millisecond},
	})
	start := time.Now()
	runner.Run(10*time.Millisecond, 0, 2)
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Errorf("waited %v, want >= 400ms", elapsed)
	}
}

func Test_writes_within_one_cycle_are_spaced(t *testing.T) {
	plan := testPlan()
	for _, key := range []string{"a", "b", "c"} {
		plan.Services = append(plan.Services, model.ServiceData{
			Key: key, InternalPort: 1,
			SrvName: "_" + key + "._tcp.example.com", Protocol: "tcp",
			Weight: 5, Record: model.RecordSrv,
		})
	}
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": 1, "a": 2, "b": 3, "c": 4})}
	runner := buildTestAgent(t, src, &fakeProviderTest{}, plan, filepath.Join(t.TempDir(), "s.json"), agent.OptionsData{})
	start := time.Now()
	runner.Tick(false)
	if elapsed := time.Since(start); elapsed < 4*agent.WriteSpacing {
		t.Errorf("five writes took %v, want >= %v", elapsed, 4*agent.WriteSpacing)
	}
}

func Test_first_write_of_cycle_is_not_delayed(t *testing.T) {
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": 51471})}
	runner := newTestAgent(t, src, &fakeProviderTest{})
	runner.Tick(false)
	src.data = endpoints("5.6.7.8", map[string]int{"mc": 51471})
	start := time.Now()
	runner.Tick(false)
	if elapsed := time.Since(start); elapsed > agent.WriteSpacing {
		t.Errorf("single-write cycle took %v", elapsed)
	}
}

func startRelayBlocker(t *testing.T) (net.Listener, int) {
	t.Helper()
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { blocker.Close() })
	return blocker, blocker.Addr().(*net.TCPAddr).Port
}

func Test_port_not_published_when_relay_cannot_bind(t *testing.T) {
	_, taken := startRelayBlocker(t)
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": taken})}
	prov := &fakeProviderTest{}
	runner := buildTestAgent(t, src, prov, testPlan(), filepath.Join(t.TempDir(), "s.json"),
		agent.OptionsData{Relay: true, BindHost: "127.0.0.1", Log: func(string) {}})
	defer runner.Stop()

	result := runner.Tick(false)
	for _, call := range prov.srvCalls {
		if call.Port == taken {
			t.Errorf("published port %d after bind failure", taken)
		}
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "mc" {
		t.Errorf("expected mc skipped, got %v", result.Skipped)
	}
}

func Test_failed_relay_recovers_when_port_frees(t *testing.T) {
	blocker, taken := startRelayBlocker(t)
	src := &fakeSourceTest{data: endpoints("1.2.3.4", map[string]int{"mc": taken})}
	prov := &fakeProviderTest{}
	runner := buildTestAgent(t, src, prov, testPlan(), filepath.Join(t.TempDir(), "s.json"),
		agent.OptionsData{Relay: true, BindHost: "127.0.0.1", Log: func(string) {}})
	defer runner.Stop()

	runner.Tick(false)
	if len(prov.srvCalls) != 0 {
		t.Fatalf("should not publish while blocked: %+v", prov.srvCalls)
	}
	blocker.Close()
	runner.Tick(false)
	if len(prov.srvCalls) != 1 || prov.srvCalls[0].Port != taken {
		t.Errorf("must publish once port frees: %+v", prov.srvCalls)
	}
}
