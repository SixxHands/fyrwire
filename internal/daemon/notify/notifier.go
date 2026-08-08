package notify

import (
	"sort"
	"strconv"
	"strings"
)

const (
	colorRed   = 0xE06C6C
	colorGreen = 0x6EDC96
	colorAmber = 0xF0BE5A
)

type Notifier struct {
	AfterFailures int
	Log           func(string)
	channels      []IAlertChannel
}

type AlertStatusData struct {
	Ok       bool
	Host     string
	Error    string
	Skipped  []string
	Failures int
	KeptPort map[string]int
	Reasons  map[string]string
}

type AlertState struct {
	Down     bool
	Services []string
}

func (notifier *Notifier) send(title, body string, color int) {
	for _, channel := range notifier.channels {
		if err := channel.Send(title, body, color); err != nil && notifier.Log != nil {
			notifier.Log("alert delivery failed: " + err.Error())
		}
	}
}

func (notifier *Notifier) CheckTransitions(status AlertStatusData, state *AlertState, node string) []string {
	after := notifier.AfterFailures
	if after < 1 {
		after = 2
	}

	if !status.Ok {
		return notifier.checkNodeDown(status, state, node, after)
	}

	var sent []string
	if state.Down {
		notifier.send(node+": recovered", "Back online at `"+status.Host+"`.", colorGreen)
		state.Down = false
		sent = append(sent, "recovered")
	}

	return append(sent, notifier.checkServiceTransitions(status, state, node)...)
}

func (notifier *Notifier) checkNodeDown(status AlertStatusData, state *AlertState, node string, after int) []string {
	if status.Failures < after || state.Down {
		return nil
	}

	notifier.send(
		node+": cannot reach the gateway",
		strconv.Itoa(status.Failures)+" consecutive failures.\n`"+status.Error+"`\n\n"+
			"Leaving DNS pointed at the last known good endpoint"+formatKeptPorts(status.KeptPort)+".",
		colorRed,
	)
	state.Down = true
	return []string{"down"}
}

func (notifier *Notifier) checkServiceTransitions(status AlertStatusData, state *AlertState, node string) []string {
	var sent []string
	nowSkipped := map[string]bool{}
	for _, key := range status.Skipped {
		nowSkipped[key] = true
	}

	known := map[string]bool{}
	for _, key := range state.Services {
		known[key] = true
	}

	for _, key := range sortedKeys(nowSkipped) {
		if !known[key] {
			reason := "the gateway granted it no port"
			if found, ok := status.Reasons[key]; ok && found != "" {
				reason = found
			}
			notifier.send(
				node+": "+key+" is not being published",
				reason+".\n\nIts record is unchanged; the other services are fine.",
				colorAmber,
			)
			sent = append(sent, "service-down:"+key)
		}
	}

	for _, key := range sortedKeys(known) {
		if !nowSkipped[key] {
			notifier.send(node+": "+key+" is back", "Port updated and published again.", colorGreen)
			sent = append(sent, "service-up:"+key)
		}
	}

	state.Services = sortedKeys(nowSkipped)
	return sent
}

func formatKeptPorts(ports map[string]int) string {
	if len(ports) == 0 {
		return ""
	}

	keys := make([]string, 0, len(ports))
	for key := range ports {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+":"+strconv.Itoa(ports[key]))
	}

	return " (" + strings.Join(parts, ", ") + ")"
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
