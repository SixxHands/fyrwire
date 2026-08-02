package notify

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	red   = 0xE06C6C
	green = 0x6EDC96
	amber = 0xF0BE5A
)

type Notifier struct {
	Webhook       string
	Command       string
	AfterFailures int
	Timeout       time.Duration
	Log           func(string)
}

func (n *Notifier) send(title, body string, colour int) {
	if n.Timeout == 0 {
		n.Timeout = 8 * time.Second
	}
	if n.Webhook != "" {
		if err := n.post(title, body, colour); err != nil && n.Log != nil {
			n.Log("alert delivery failed: " + err.Error())
		}
	}
	if n.Command != "" {
		cmd := strings.NewReplacer(
			"{title}", title,
			"{body}", strings.ReplaceAll(body, "\n", " ")).Replace(n.Command)
		c := exec.Command("sh", "-c", cmd)
		var stderr bytes.Buffer
		c.Stderr = &stderr
		if err := c.Run(); err != nil && n.Log != nil {

			msg := "alert command failed: " + err.Error()
			if line := firstLine(stderr.String()); line != "" {
				msg += " — " + line
			}
			n.Log(msg)
		}
	}
}

// Formats for Discord, Slack, or a plain webhook. Redirects are refused on purpose so nobody bounces our token somewhere funny.
func (n *Notifier) post(title, body string, colour int) error {
	var payload any
	switch {
	case strings.Contains(n.Webhook, "discord.com/api/webhooks"):
		payload = map[string]any{"embeds": []map[string]any{
			{"title": title, "description": body, "color": colour}}}
	case strings.Contains(n.Webhook, "hooks.slack.com"):
		payload = map[string]any{"text": "*" + title + "*\n" + body}
	default:
		payload = map[string]any{"title": title, "text": body}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	client := &http.Client{
		Timeout: n.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Post(n.Webhook, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return errStatus{code: resp.StatusCode}
	}
	return nil
}

type errStatus struct{ code int }

func (e errStatus) Error() string {
	return "webhook returned HTTP " + strconv.Itoa(e.code) +
		" — the alert was NOT delivered"
}

type AlertStatusData struct {
	OK       bool
	Host     string
	Error    string
	Skipped  []string
	Failures int
	KeptPort map[string]int

	Reasons map[string]string
}

type AlertState struct {
	Down     bool
	Services []string
}

func (n *Notifier) Evaluate(s AlertStatusData, st *AlertState, node string) []string {
	var sent []string
	after := n.AfterFailures
	if after < 1 {
		after = 2
	}

	if !s.OK {
		if s.Failures >= after && !st.Down {
			detail := ""
			if len(s.KeptPort) > 0 {
				keys := make([]string, 0, len(s.KeptPort))
				for k := range s.KeptPort {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				parts := make([]string, 0, len(keys))
				for _, k := range keys {
					parts = append(parts, k+":"+itoa(s.KeptPort[k]))
				}
				detail = " (" + strings.Join(parts, ", ") + ")"
			}
			n.send(node+" — cannot reach the gateway",
				itoa(s.Failures)+" consecutive failures.\n`"+s.Error+"`\n\n"+
					"Leaving DNS pointed at the last known good endpoint"+detail+".",
				red)
			st.Down = true
			sent = append(sent, "down")
		}
		return sent
	}

	if st.Down {
		n.send(node+" — recovered", "Resolving again at `"+s.Host+"`.", green)
		st.Down = false
		sent = append(sent, "recovered")
	}

	nowSkipped := map[string]bool{}
	for _, k := range s.Skipped {
		nowSkipped[k] = true
	}
	known := map[string]bool{}
	for _, k := range st.Services {
		known[k] = true
	}
	for _, k := range sortedKeys(nowSkipped) {
		if !known[k] {
			why := "the gateway granted it no port"
			if r, ok := s.Reasons[k]; ok && r != "" {
				why = r
			}
			n.send(node+" — "+k+" is not being published",
				why+".\n\nIts record is unchanged; the other services are fine.",
				amber)
			sent = append(sent, "service-down:"+k)
		}
	}
	for _, k := range sortedKeys(known) {
		if !nowSkipped[k] {
			n.send(node+" — "+k+" is back",
				"It resolved again and its record has been updated.", green)
			sent = append(sent, "service-up:"+k)
		}
	}
	st.Services = sortedKeys(nowSkipped)
	return sent
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.TrimSpace(s)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
