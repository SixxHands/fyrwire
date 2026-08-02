package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sixxhands/fyrwire/internal/hostinfo"
	"github.com/sixxhands/fyrwire/internal/model"
	"github.com/sixxhands/fyrwire/internal/notify"
	"github.com/sixxhands/fyrwire/internal/provider"
	"github.com/sixxhands/fyrwire/internal/relay"
	"github.com/sixxhands/fyrwire/internal/source"
)

type AgentState struct {
	LastHost            string         `json:"last_host"`
	LastPorts           map[string]int `json:"last_ports"`
	ConsecutiveFailures int            `json:"consecutive_failures"`
	TotalPublishes      int            `json:"total_publishes"`
	LastChangeAt        string         `json:"last_change_at"`
	LastError           string         `json:"last_error"`
	AlertedDown         bool           `json:"alerted_down"`
	AlertedServices     []string       `json:"alerted_services"`

	Published map[string]string `json:"published"`

	LastVerified int64 `json:"last_verified"`
}

func LoadState(path string) *AgentState {
	s := &AgentState{LastPorts: map[string]int{}, Published: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, s); err != nil {

		return &AgentState{LastPorts: map[string]int{}, Published: map[string]string{}}
	}
	if s.LastPorts == nil {
		s.LastPorts = map[string]int{}
	}
	if s.Published == nil {
		s.Published = map[string]string{}
	}
	return s
}

func (s *AgentState) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type OptionsData struct {
	Relay      bool
	RelayUDP   bool
	BindHost   string
	TargetHost string
	UDP        relay.UdpOptionsData
	Log        func(string)
	Now        func() time.Time

	Notifier *notify.Notifier
	NodeName string
}

type Agent struct {
	src       source.IPortSource
	prov      provider.IDnsProvider
	plan      model.NodePlanData
	statePath string
	State     *AgentState
	opt       OptionsData
	relays    *relay.Manager
	lastWrite time.Time
	warned    map[string]bool
}

func (a *Agent) warnOnce(msg string) {
	if a.warned == nil {
		a.warned = map[string]bool{}
	}
	if a.warned[msg] {
		return
	}
	a.warned[msg] = true
	a.opt.Log(msg)
}

const writeSpacing = 550 * time.Millisecond

const reverifyAfter = 15 * time.Minute

func aPrint(name, host string, ttl int) string {
	return fmt.Sprintf("%s|%s|%d", name, host, ttl)
}

func (a *Agent) svcPrint(s model.Service, port int) string {
	ttl := s.TTL
	if ttl == 0 {
		ttl = a.plan.TTL
	}
	return fmt.Sprintf("%s|%s|%d|%s|%d|%d|%d|%s", s.SrvName, a.plan.AName,
		port, s.Protocol, s.Priority, s.Weight, ttl, s.Record)
}

func New(src source.IPortSource, prov provider.IDnsProvider, plan model.NodePlanData, statePath string, opt OptionsData) *Agent {
	if opt.Log == nil {
		opt.Log = func(string) {}
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.TargetHost == "" {
		opt.TargetHost = "127.0.0.1"
	}
	a := &Agent{src: src, prov: prov, plan: plan, statePath: statePath,
		State: LoadState(statePath), opt: opt}
	if opt.Relay {
		a.relays = relay.NewManager(opt.Log, opt.UDP)
	}
	return a
}

func (a *Agent) paced(fn func() (string, error)) (string, error) {
	if !a.lastWrite.IsZero() {
		if gap := time.Since(a.lastWrite); gap < writeSpacing {
			time.Sleep(writeSpacing - gap)
		}
	}
	defer func() { a.lastWrite = time.Now() }()
	return fn()
}

type TickResult struct {
	OK      bool
	Host    string
	Changes []string
	Skipped []string

	Reasons   map[string]string
	Err       error
	Unchanged bool
}

func (a *Agent) Tick(force bool) TickResult {
	res, err := a.src.Resolve(a.plan.Requests())
	if err != nil {

		return a.fail(err, a.State.LastHost)
	}

	a.lastWrite = time.Time{}

	if time.Since(time.Unix(a.State.LastVerified, 0)) > reverifyAfter {
		force = true
	}

	if r, ok := a.src.(interface{ TookRestart() bool }); ok && r.TookRestart() {
		force = true
	}

	var changes []string
	skipped := make([]string, 0, len(res.Partial))
	for k := range res.Partial {
		skipped = append(skipped, k)
	}
	sort.Strings(skipped)

	relayChanges, relayFailed := a.syncRelays(res)
	changes = append(changes, relayChanges...)

	skip := map[string]bool{}
	reasons := map[string]string{}
	for k := range res.Partial {
		skip[k] = true
		reasons[k] = "the gateway granted it no port"
	}
	for key, why := range relayFailed {
		if !skip[key] {
			skipped = append(skipped, key)
		}
		skip[key] = true

		reasons[key] = why
	}
	sort.Strings(skipped)

	aFP := aPrint(a.plan.AName, res.Host, a.plan.TTL)
	if force || aFP != a.State.Published["@a"] {
		what, err := a.paced(func() (string, error) {
			return a.prov.EnsureA(a.plan.AName, res.Host, a.plan.TTL)
		})
		if err != nil {
			return a.fail(err, res.Host)
		}
		if what != "" {
			changes = append(changes, what)
		}
		a.State.LastHost = res.Host
		a.State.Published["@a"] = aFP
	}

	for _, svc := range a.plan.Services {
		port, ok := res.Ports[svc.Key]
		if !ok || skip[svc.Key] {

			continue
		}
		fp := a.svcPrint(svc, port)
		if !force && a.State.Published[svc.Key] == fp {
			continue
		}
		ttl := svc.TTL
		if ttl == 0 {
			ttl = a.plan.TTL
		}
		var what string
		var err error
		switch svc.Record {
		case model.RecordNone:

			a.State.LastPorts[svc.Key] = port
			a.State.Published[svc.Key] = fp
			continue
		case model.RecordHTTPS:
			what, err = a.paced(func() (string, error) {
				return a.prov.EnsureHTTPS(model.HttpsRecordData{Name: svc.SrvName,
					Target: a.plan.AName, Port: port, Priority: 1, TTL: ttl})
			})
		default:
			what, err = a.paced(func() (string, error) {
				return a.prov.EnsureSRV(model.SrvRecordData{Name: svc.SrvName,
					Target: a.plan.AName, Port: port, Priority: svc.Priority,
					Weight: svc.Weight, TTL: ttl})
			})
		}
		if err != nil {
			return a.fail(err, res.Host)
		}
		if what != "" {
			changes = append(changes, what)
		}
		a.State.LastPorts[svc.Key] = port
		a.State.Published[svc.Key] = fp
	}

	a.alert(TickResult{OK: true, Host: res.Host, Skipped: skipped, Reasons: reasons})

	if a.State.ConsecutiveFailures > 0 {
		a.opt.Log(fmt.Sprintf("recovered after %d failure(s)",
			a.State.ConsecutiveFailures))
	}
	a.State.ConsecutiveFailures = 0
	a.State.LastError = ""
	a.State.LastVerified = time.Now().Unix()

	if len(changes) > 0 {
		a.State.TotalPublishes++
		a.State.LastChangeAt = a.opt.Now().UTC().Format(time.RFC3339)
	}

	_ = a.State.Save(a.statePath)
	return TickResult{OK: true, Host: res.Host, Changes: changes, Skipped: skipped,
		Reasons: reasons, Unchanged: len(changes) == 0}
}

// Never wipe DNS on a bad cycle. A VPN blip should leave the last working port alone instead of taking everything down.
func (a *Agent) fail(err error, host string) TickResult {
	a.State.ConsecutiveFailures++
	a.State.LastError = err.Error()
	r := TickResult{OK: false, Err: err, Host: host}

	a.alert(r)
	_ = a.State.Save(a.statePath)
	return r
}

func (a *Agent) alert(r TickResult) {
	if a.opt.Notifier == nil {
		return
	}
	st := &notify.AlertState{Down: a.State.AlertedDown,
		Services: a.State.AlertedServices}
	errText := ""
	if r.Err != nil {
		errText = r.Err.Error()
	}
	a.opt.Notifier.Evaluate(notify.AlertStatusData{
		OK: r.OK, Host: r.Host, Error: errText, Skipped: r.Skipped,
		Reasons:  r.Reasons,
		Failures: a.State.ConsecutiveFailures, KeptPort: a.State.LastPorts,
	}, st, a.opt.NodeName)
	a.State.AlertedDown, a.State.AlertedServices = st.Down, st.Services
}

func (a *Agent) syncRelays(res model.EndpointData) ([]string, map[string]string) {
	failed := map[string]string{}
	if a.relays == nil {
		return nil, failed
	}
	bind := a.opt.BindHost
	if bind == "" {

		if b, ok := a.src.(interface{ BindAddress() string }); ok {
			bind = b.BindAddress()
		}
		if egress := hostinfo.EgressAddress(); bind == "" {
			bind = egress
		} else if egress != "" && egress != bind {
			a.warnOnce(fmt.Sprintf(
				"binding %s (where the port forward arrives) rather than %s "+
					"(the route to the internet) — they differ on this host",
				bind, egress))
		}
	}
	if bind == "" {
		a.opt.Log("relay: no tunnel address found; skipping")
		return nil, failed
	}
	wanted := map[string]relay.BindingData{}
	for _, svc := range a.plan.Services {
		port, ok := res.Ports[svc.Key]
		if !ok {
			continue
		}
		if svc.Protocol == "udp" && !a.opt.RelayUDP {

			continue
		}
		wanted[svc.Key] = relay.BindingData{BindHost: bind, BindPort: port,
			TargetHost: a.opt.TargetHost, TargetPort: svc.InternalPort,
			Protocol: svc.Protocol}
	}
	changes := a.relays.Sync(wanted)
	live := a.relays.Stats()
	for key, spec := range wanted {
		if _, ok := live[key]; !ok {
			failed[key] = fmt.Sprintf(
				"its relay could not bind %s:%d — something else is already "+
					"using that port, or the address is not on this machine",
				spec.BindHost, spec.BindPort)
		}
	}
	return changes, failed
}

// Runs until canceled or maxCycles hits. Panics get caught per cycle so one weird crash does not silently kill the daemon.
func (a *Agent) Run(interval time.Duration, jitter time.Duration, maxCycles int) {
	backoff := time.Duration(0)
	for n := 0; maxCycles <= 0 || n < maxCycles; n++ {
		wait := interval
		report := a.safeTick()
		switch {
		case report.Err == nil:
			backoff = 0
		default:
			var rl *provider.RateLimited
			if errors.As(report.Err, &rl) {

				if backoff == 0 {
					backoff = interval
				} else {
					backoff *= 2
				}

				if backoff > 15*time.Minute {
					backoff = 15 * time.Minute
				}
				wait = backoff
				if rl.RetryAfter > wait {
					wait = rl.RetryAfter
				}
				a.opt.Log(fmt.Sprintf("rate limited; waiting %s — %v", wait, rl))
			} else {

				a.opt.Log("cycle failed: " + report.Err.Error())
			}
		}
		for _, c := range report.Changes {
			a.opt.Log(c)
		}
		if maxCycles > 0 && n+1 >= maxCycles {
			return
		}

		if lease, ok := a.src.(interface{ GrantedLifetime() int }); ok {
			if secs := lease.GrantedLifetime(); secs > 0 {
				safe := time.Duration(secs) * time.Second * 2 / 3
				if wait > safe {
					a.warnOnce(fmt.Sprintf(
						"the gateway grants only %ds, so reconciling every %s "+
							"instead of %s to keep the mapping alive",
						secs, safe, wait))
					wait = safe
				}
				if jitter > safe/2 {
					jitter = safe / 2
				}
			}
		}
		if jitter > 0 {

			wait += time.Duration(rand.Int63n(int64(jitter)))
		}
		time.Sleep(wait)
	}
}

func (a *Agent) safeTick() (r TickResult) {
	defer func() {
		if p := recover(); p != nil {
			a.opt.Log(fmt.Sprintf("unexpected error: %v", p))
			r = TickResult{OK: false, Err: model.Errorf("panic: %v", p)}
		}
	}()
	return a.Tick(false)
}

func (a *Agent) Shutdown() {
	if a.relays != nil {
		a.relays.Shutdown()
	}
}

func (a *Agent) Relays() *relay.Manager { return a.relays }
