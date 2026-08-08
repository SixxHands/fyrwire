package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/sixxhands/fyrwire/internal/network/provider"
	"github.com/sixxhands/fyrwire/internal/network/relay"
	"github.com/sixxhands/fyrwire/internal/network/source"
	"github.com/sixxhands/fyrwire/internal/node/model"
)

type Agent struct {
	source    source.IPortSource
	provider  provider.IDnsProvider
	plan      model.NodePlanData
	statePath string
	State     *AgentState
	options   OptionsData
	relays    *relay.Manager
	observers []IAgentObserver
	lastWrite time.Time
	warned    map[string]bool
}

type TickResult struct {
	Ok        bool
	Host      string
	Changes   []string
	Skipped   []string
	Reasons   map[string]string
	Err       error
	Unchanged bool
}

// Space DNS writes slightly so we do not immediately get smacked in the face by API rate limits on multi-service nodes.
const (
	WriteSpacing  = 550 * time.Millisecond
	reverifyAfter = 15 * time.Minute
)

func (agent *Agent) warnOnce(message string) {
	if agent.warned == nil {
		agent.warned = map[string]bool{}
	}
	if agent.warned[message] {
		return
	}

	agent.warned[message] = true
	agent.options.Log(message)
}

func (agent *Agent) Tick(force bool) TickResult {
	endpoints, err := agent.source.FindEndpoints(agent.plan.Requests())
	if err != nil {
		return agent.failTick(err, agent.State.LastHost)
	}

	agent.lastWrite = time.Time{}
	if agent.shouldForcePublish(force) {
		force = true
	}

	// Bind the local relay BEFORE telling DNS about the new port. Publishing a dead port is a bad ending.
	relayChanges, relayFailed := agent.syncRelays(endpoints)
	skip, skipped, reasons := collectSkipped(endpoints.Partial, relayFailed)

	changes, err := agent.publishRecords(endpoints, skip, force)
	if err != nil {
		return agent.failTick(err, endpoints.Host)
	}

	changes = append(relayChanges, changes...)
	return agent.finishTick(endpoints.Host, changes, skipped, reasons)
}

func (agent *Agent) shouldForcePublish(force bool) bool {
	if force || time.Since(time.Unix(agent.State.LastVerified, 0)) > reverifyAfter {
		return true
	}

	if restartSource, ok := agent.source.(interface{ TookRestart() bool }); ok && restartSource.TookRestart() {
		return true
	}

	return false
}

func (agent *Agent) finishTick(host string, changes, skipped []string, reasons map[string]string) TickResult {
	result := TickResult{
		Ok:        true,
		Host:      host,
		Changes:   changes,
		Skipped:   skipped,
		Reasons:   reasons,
		Unchanged: len(changes) == 0,
	}
	agent.notifyObservers(result)

	if agent.State.ConsecutiveFailures > 0 {
		agent.options.Log(fmt.Sprintf("recovered after %d failure(s)", agent.State.ConsecutiveFailures))
	}

	agent.State.ConsecutiveFailures = 0
	agent.State.LastError = ""
	agent.State.LastVerified = time.Now().Unix()

	if len(changes) > 0 {
		agent.State.TotalPublishes++
		agent.State.LastChangeAt = agent.options.Now().UTC().Format(time.RFC3339)
	}

	_ = agent.State.Save(agent.statePath)
	return result
}

// Never wipe DNS on a bad cycle. A VPN blip should leave the last working port alone instead of taking everything down.
// Had this happen more than once and its annoying to deal with by hand.
func (agent *Agent) failTick(err error, host string) TickResult {
	agent.State.ConsecutiveFailures++
	agent.State.LastError = err.Error()

	result := TickResult{Ok: false, Err: err, Host: host}
	agent.notifyObservers(result)
	_ = agent.State.Save(agent.statePath)
	return result
}

func (agent *Agent) notifyObservers(result TickResult) {
	for _, observer := range agent.observers {
		if observer != nil {
			observer.OnTick(result, agent.State)
		}
	}
}

func (agent *Agent) Run(interval, jitter time.Duration, maxCycles int) {
	agent.RunContext(context.Background(), interval, jitter, maxCycles)
}

// Runs until canceled or maxCycles hits. Panics should be caught per cycle so one weird crash does not silently kill the daemon.
func (agent *Agent) RunContext(ctx context.Context, interval, jitter time.Duration, maxCycles int) {
	backoff := time.Duration(0)
	for cycle := 0; maxCycles <= 0 || cycle < maxCycles; cycle++ {
		if ctx.Err() != nil {
			return
		}

		result := agent.safeTick()
		wait := agent.nextWait(interval, &backoff, result)
		for _, change := range result.Changes {
			agent.options.Log(change)
		}

		if maxCycles > 0 && cycle+1 >= maxCycles {
			return
		}

		wait = agent.capWaitToLease(wait, jitter)
		if !sleepContext(ctx, wait) {
			return
		}
	}
}

func (agent *Agent) nextWait(interval time.Duration, backoff *time.Duration, result TickResult) time.Duration {
	if result.Err == nil {
		*backoff = 0
		return interval
	}

	var limited *provider.RateLimited
	if errors.As(result.Err, &limited) {
		if *backoff == 0 {
			*backoff = interval
		} else {
			*backoff *= 2
		}

		if *backoff > 15*time.Minute {
			*backoff = 15 * time.Minute
		}

		wait := *backoff
		if limited.RetryAfter > wait {
			wait = limited.RetryAfter
		}

		agent.options.Log(fmt.Sprintf("rate limited; waiting %s (%v)", wait, limited))
		return wait
	}

	agent.options.Log("cycle failed: " + result.Err.Error())
	return interval
}

func (agent *Agent) capWaitToLease(wait, jitter time.Duration) time.Duration {
	if lease, ok := agent.source.(interface{ GrantedLifetime() int }); ok {
		if seconds := lease.GrantedLifetime(); seconds > 0 {
			safe := time.Duration(seconds) * time.Second * 2 / 3
			if wait > safe {
				agent.warnOnce(fmt.Sprintf("gateway granted %ds lease; updating every %s instead of %s",
					seconds, safe, wait))
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

	return wait
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	select {
	case <-ctx.Done():
		timer.Stop()
		return false
	case <-timer.C:
		return true
	}
}

func (agent *Agent) safeTick() (result TickResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			agent.options.Log(fmt.Sprintf("unexpected error: %v", recovered))
			result = TickResult{Ok: false, Err: model.Errorf("panic: %v", recovered)}
		}
	}()

	return agent.Tick(false)
}

func (agent *Agent) Stop() {
	if agent.relays != nil {
		agent.relays.Stop()
	}
}

func (agent *Agent) Relays() *relay.Manager {
	return agent.relays
}

// wooo end of file!
