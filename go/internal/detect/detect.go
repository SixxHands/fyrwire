package detect

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sixxhands/fyrwire/internal/hostinfo"
	"github.com/sixxhands/fyrwire/internal/model"
	"github.com/sixxhands/fyrwire/internal/source"
)

func GatewayCandidates() [][2]string {
	var out [][2]string
	seen := map[string]bool{}
	add := func(addr, why string) {
		if addr == "" || seen[addr] {
			return
		}
		seen[addr] = true
		out = append(out, [2]string{addr, why})
	}
	for _, i := range hostinfo.Interfaces() {
		if i.Tunnelish() {
			add(hostinfo.SubnetRouter(i.Address), "tunnel "+i.Name)
		}
	}
	if egress := hostinfo.EgressAddress(); egress != "" {
		add(hostinfo.SubnetRouter(egress), "the route to the internet")
	}
	for _, i := range hostinfo.Interfaces() {
		if !i.Tunnelish() {
			add(hostinfo.SubnetRouter(i.Address), "interface "+i.Name)
		}
	}
	return out
}

func Probe(gateway string) string {
	n := &source.NatPmpSource{Gateway: gateway, Timeout: 1.5, Retries: 1}
	addr, err := n.PublicAddress()
	if err != nil {
		return ""
	}
	return addr
}

func FindGateway() (gateway, public, why string) {
	for _, c := range GatewayCandidates() {
		if pub := Probe(c[0]); pub != "" {
			return c[0], pub, c[1]
		}
	}
	return "", "", ""
}

type DomainData struct {
	Name       string
	MinimumTTL int
}

func DesecDomains(token string) ([]DomainData, error) {
	body, err := get("https://desec.io/api/v1/domains/", "Token "+token)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Name       string `json:"name"`
		MinimumTTL int    `json:"minimum_ttl"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, model.Errorf("desec sent an unreadable domain list")
	}
	out := make([]DomainData, 0, len(rows))
	for _, r := range rows {
		out = append(out, DomainData{Name: r.Name, MinimumTTL: r.MinimumTTL})
	}
	return out, nil
}

type ZoneData struct {
	ID   string
	Name string
}

func CloudflareZones(token string) ([]ZoneData, error) {
	body, err := get(
		"https://api.cloudflare.com/client/v4/zones?per_page=50", "Bearer "+token)
	if err != nil {
		return nil, err
	}
	var env struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Result []ZoneData `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, model.Errorf("cloudflare sent unreadable JSON")
	}

	if !env.Success {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		return nil, model.Errorf("cloudflare rejected the token: %s",
			strings.Join(msgs, "; "))
	}
	return env.Result, nil
}

func get(url, auth string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, model.Errorf("cannot reach the DNS provider: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, model.Errorf("the token was rejected (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, model.Errorf("HTTP %d from the DNS provider", resp.StatusCode)
	}
	return body, nil
}

func ListeningPorts() []hostinfo.Port {
	ports, _ := hostinfo.Current().ListeningPorts()
	return ports
}
