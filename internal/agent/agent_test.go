package agent

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sixxhands/fyrwire/internal/model"
	"github.com/sixxhands/fyrwire/internal/provider"
	"github.com/sixxhands/fyrwire/internal/relay"
)

type fakeSource struct {
	res  model.EndpointData
	err  error
	hits int
}

func (f *fakeSource) Name() string { return "fake" }
func (f *fakeSource) Resolve([]model.PortRequestData) (model.EndpointData, error) {
	f.hits++
	return f.res, f.err
}

type fakeProvider struct {
	aCalls   []string
	srvCalls []model.SrvRecordData
	err      error
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) EnsureA(name, ip string, ttl int) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.aCalls = append(f.aCalls, name+"="+ip)
	return "set A " + name, nil
}
func (f *fakeProvider) EnsureSRV(r model.SrvRecordData) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.srvCalls = append(f.srvCalls, r)
	return "set SRV " + r.Name, nil
}
func (f *fakeProvider) EnsureHTTPS(r model.HttpsRecordData) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "set HTTPS " + r.Name, nil
}
func (f *fakeProvider) Delete(rtype, name, expect string) (string, error) {
	return "", nil
}

func plan() model.NodePlanData {
	return model.NodePlanData{AName: "node.example.com", TTL: 60,
		Services: []model.Service{{Key: "mc", InternalPort: 25565,
			SrvName: "_minecraft._tcp.example.com", Protocol: "tcp",
			Weight: 5, Record: model.RecordSRV}}}
}

func newTestAgent(t *testing.T, src *fakeSource, prov *fakeProvider) *Agent {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "state.json")
	return New(src, prov, plan(), statePath, OptionsData{})
}

func res(host string, ports map[string]int) model.EndpointData {
	return model.EndpointData{Host: host, Ports: ports, Partial: map[string]bool{}}
}

func TestPublishesOnlyOnChange(t *testing.T) {
	src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": 51471})}
	prov := &fakeProvider{}
	a := newTestAgent(t, src, prov)

	first := a.Tick(false)
	if len(first.Changes) == 0 {
		t.Fatal("the first tick must publish")
	}
	second := a.Tick(false)
	if len(second.Changes) != 0 {
		t.Errorf("nothing changed, so nothing should publish: %v", second.Changes)
	}
	if len(prov.aCalls) != 1 || len(prov.srvCalls) != 1 {
		t.Errorf("A=%d SRV=%d, want 1 each", len(prov.aCalls), len(prov.srvCalls))
	}
}

func TestARotatedPortIsPublished(t *testing.T) {
	src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": 51471})}
	prov := &fakeProvider{}
	a := newTestAgent(t, src, prov)
	a.Tick(false)
	src.res = res("1.2.3.4", map[string]int{"mc": 40000})
	a.Tick(false)
	if len(prov.srvCalls) != 2 {
		t.Fatalf("SRV calls = %d", len(prov.srvCalls))
	}
	if prov.srvCalls[1].Port != 40000 {
		t.Errorf("published port %d", prov.srvCalls[1].Port)
	}

	if len(prov.aCalls) != 1 {
		t.Errorf("A rewritten %d times for an unchanged address", len(prov.aCalls))
	}
}

func TestAnAddressChangeDoesNotRewriteEverySRV(t *testing.T) {
	src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": 51471})}
	prov := &fakeProvider{}
	a := newTestAgent(t, src, prov)
	a.Tick(false)
	src.res = res("5.6.7.8", map[string]int{"mc": 51471})
	a.Tick(false)
	if len(prov.aCalls) != 2 {
		t.Errorf("the address changed, so A must be republished: %v", prov.aCalls)
	}
	if len(prov.srvCalls) != 1 {
		t.Errorf("the port did not change, so SRV must be left alone: %d",
			len(prov.srvCalls))
	}
}

func TestAFailureNeverClearsDNS(t *testing.T) {
	src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": 51471})}
	prov := &fakeProvider{}
	a := newTestAgent(t, src, prov)
	a.Tick(false)
	before := len(prov.aCalls) + len(prov.srvCalls)

	src.err = model.Errorf("gateway unreachable")
	r := a.Tick(false)
	if r.OK {
		t.Fatal("expected a failure")
	}
	if after := len(prov.aCalls) + len(prov.srvCalls); after != before {
		t.Errorf("a failing cycle touched DNS: %d -> %d", before, after)
	}
	if a.State.LastHost != "1.2.3.4" {
		t.Errorf("last known good endpoint was forgotten: %q", a.State.LastHost)
	}
}

func TestOnePartialServiceDoesNotBlockTheOthers(t *testing.T) {
	p := plan()
	p.Services = append(p.Services, model.Service{Key: "vh", InternalPort: 2456,
		SrvName: "_valheim._udp.example.com", Protocol: "udp", Weight: 5,
		Record: model.RecordSRV})
	src := &fakeSource{res: model.EndpointData{Host: "1.2.3.4",
		Ports: map[string]int{"mc": 51471}, Partial: map[string]bool{"vh": true}}}
	prov := &fakeProvider{}
	a := New(src, prov, p, filepath.Join(t.TempDir(), "s.json"), OptionsData{})
	r := a.Tick(false)
	if !r.OK {
		t.Fatalf("a partial resolution is not a node failure: %v", r.Err)
	}
	if len(prov.srvCalls) != 1 || prov.srvCalls[0].Name != "_minecraft._tcp.example.com" {
		t.Errorf("the working service must publish: %+v", prov.srvCalls)
	}
	if len(r.Skipped) != 1 || r.Skipped[0] != "vh" {
		t.Errorf("the failed service must be reported as skipped: %v", r.Skipped)
	}
}

func TestStateSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": 51471})}
	prov := &fakeProvider{}
	New(src, prov, plan(), statePath, OptionsData{}).Tick(false)

	prov2 := &fakeProvider{}
	a2 := New(src, prov2, plan(), statePath, OptionsData{})
	if r := a2.Tick(false); len(r.Changes) != 0 {
		t.Errorf("restart republished unnecessarily: %v", r.Changes)
	}
}

func TestACorruptStateFileIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := writeFile(statePath, "{not json"); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": 51471})}
	prov := &fakeProvider{}
	a := New(src, prov, plan(), statePath, OptionsData{})
	if r := a.Tick(false); !r.OK {
		t.Errorf("a corrupt state file stopped the agent: %v", r.Err)
	}
}

func TestUdpIsNotRelayedUnlessAskedFor(t *testing.T) {
	p := plan()
	p.Services[0].Protocol = "udp"
	src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": 51471})}
	a := New(src, &fakeProvider{}, p, filepath.Join(t.TempDir(), "s.json"),
		OptionsData{Relay: true, RelayUDP: false, BindHost: "127.0.0.1",
			UDP: relay.UdpOptionsData{}})
	defer a.Shutdown()
	a.Tick(false)
	if _, present := a.Relays().Stats()["mc"]; present {
		t.Error("a udp service must not be relayed with relay_udp off")
	}
}

func TestTheLoopSurvivesARateLimit(t *testing.T) {
	src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": 51471})}
	prov := &fakeProvider{err: &provider.RateLimited{Msg: "slow down"}}
	a := newTestAgent(t, src, prov)
	done := make(chan struct{})
	go func() {
		a.Run(time.Millisecond, 0, 3)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return — a rate limit must not stall the loop")
	}
	if src.hits < 3 {
		t.Errorf("the loop stopped early after %d cycles", src.hits)
	}
}

func TestARateLimitBacksOffAndAGenericErrorDoesNot(t *testing.T) {
	measure := func(err error) time.Duration {
		src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": 51471})}
		a := newTestAgent(t, src, &fakeProvider{err: err})
		start := time.Now()
		a.Run(80*time.Millisecond, 0, 3)
		return time.Since(start)
	}
	generic := measure(model.Errorf("bad token"))
	limited := measure(&provider.RateLimited{Msg: "slow down"})
	if limited <= generic {
		t.Errorf("a rate limit must back off: rate-limited=%v, generic=%v",
			limited, generic)
	}
}

func TestRetryAfterIsHonouredWhenItIsLongerThanTheBackoff(t *testing.T) {
	src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": 51471})}
	a := newTestAgent(t, src, &fakeProvider{
		err: &provider.RateLimited{Msg: "slow", RetryAfter: 400 * time.Millisecond}})
	start := time.Now()
	a.Run(10*time.Millisecond, 0, 2)
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Errorf("waited %v, but the provider asked for 400ms", elapsed)
	}
}

func TestWritesWithinOneCycleAreSpaced(t *testing.T) {
	p := plan()
	for _, k := range []string{"a", "b", "c"} {
		p.Services = append(p.Services, model.Service{Key: k, InternalPort: 1,
			SrvName: "_" + k + "._tcp.example.com", Protocol: "tcp",
			Weight: 5, Record: model.RecordSRV})
	}
	ports := map[string]int{"mc": 1, "a": 2, "b": 3, "c": 4}
	src := &fakeSource{res: res("1.2.3.4", ports)}
	a := New(src, &fakeProvider{}, p, filepath.Join(t.TempDir(), "s.json"),
		OptionsData{})
	start := time.Now()
	a.Tick(false)

	if elapsed := time.Since(start); elapsed < 4*writeSpacing {
		t.Errorf("five writes took %v — they are not being spaced, so a "+
			"reconnect will trip the provider's rate limit", elapsed)
	}
}

func TestTheFirstWriteOfACycleIsNotDelayed(t *testing.T) {
	src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": 51471})}
	a := newTestAgent(t, src, &fakeProvider{})
	a.Tick(false)
	src.res = res("5.6.7.8", map[string]int{"mc": 51471})
	start := time.Now()
	a.Tick(false)
	if elapsed := time.Since(start); elapsed > writeSpacing {
		t.Errorf("a single-write cycle took %v — pacing is leaking across "+
			"ticks", elapsed)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestAPortIsNotPublishedWhenItsRelayCannotBind(t *testing.T) {

	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	taken := blocker.Addr().(*net.TCPAddr).Port

	src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": taken})}
	prov := &fakeProvider{}
	a := New(src, prov, plan(), filepath.Join(t.TempDir(), "s.json"),
		OptionsData{Relay: true, BindHost: "127.0.0.1", Log: func(string) {}})
	defer a.Shutdown()

	r := a.Tick(false)
	for _, c := range prov.srvCalls {
		if c.Port == taken {
			t.Errorf("published port %d for a service whose relay failed to bind",
				taken)
		}
	}
	found := false
	for _, s := range r.Skipped {
		if s == "mc" {
			found = true
		}
	}
	if !found {
		t.Errorf("a service with no working relay must be reported as skipped, "+
			"so alerting sees it: %v", r.Skipped)
	}
}

func TestAPortIsPublishedWhenItsRelayBinds(t *testing.T) {
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := free.Addr().(*net.TCPAddr).Port
	free.Close()

	src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": port})}
	prov := &fakeProvider{}
	a := New(src, prov, plan(), filepath.Join(t.TempDir(), "s.json"),
		OptionsData{Relay: true, BindHost: "127.0.0.1", Log: func(string) {}})
	defer a.Shutdown()

	a.Tick(false)
	if len(prov.srvCalls) != 1 || prov.srvCalls[0].Port != port {
		t.Errorf("a working relay must publish its port: %+v", prov.srvCalls)
	}
}

func TestAFailedRelayRecoversOnceThePortIsFree(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	taken := blocker.Addr().(*net.TCPAddr).Port

	src := &fakeSource{res: res("1.2.3.4", map[string]int{"mc": taken})}
	prov := &fakeProvider{}
	a := New(src, prov, plan(), filepath.Join(t.TempDir(), "s.json"),
		OptionsData{Relay: true, BindHost: "127.0.0.1", Log: func(string) {}})
	defer a.Shutdown()

	a.Tick(false)
	if len(prov.srvCalls) != 0 {
		t.Fatalf("should not have published: %+v", prov.srvCalls)
	}
	blocker.Close()
	a.Tick(false)
	if len(prov.srvCalls) != 1 || prov.srvCalls[0].Port != taken {
		t.Errorf("must publish once the relay can bind: %+v", prov.srvCalls)
	}
}
