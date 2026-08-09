package detect

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sixxhands/fyrwire/internal/network/hostinfo"
	"github.com/sixxhands/fyrwire/internal/network/source/gateway"
	"github.com/sixxhands/fyrwire/internal/node/model"
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
		if i.IsTunnel() {
			add(hostinfo.SubnetRouter(i.Address), "tunnel "+i.Name)
		}
	}
	if egress := hostinfo.EgressAddress(); egress != "" {
		add(hostinfo.SubnetRouter(egress), "default route")
	}
	for _, i := range hostinfo.Interfaces() {
		if !i.IsTunnel() {
			add(hostinfo.SubnetRouter(i.Address), "interface "+i.Name)
		}
	}
	return out
}

func CheckGateway(gatewayAddress string) string {
	n := &gateway.NatPmpSource{Gateway: gatewayAddress, Timeout: 1.5, Retries: 1}
	addr, err := n.PublicAddress()
	if err != nil {
		return ""
	}
	return addr
}

func FindGateway() (gateway, public, why string) {
	for _, c := range GatewayCandidates() {
		if pub := CheckGateway(c[0]); pub != "" {
			return c[0], pub, c[1]
		}
	}
	return "", "", ""
}

type DomainData struct {
	Name       string
	MinimumTtl int
}

func DesecDomains(token string) ([]DomainData, error) {
	body, err := fetchJson("https://desec.io/api/v1/domains/", "Token "+token)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Name       string `json:"name"`
		MinimumTtl int    `json:"minimum_ttl"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, model.Errorf("desec sent an unreadable domain list")
	}
	out := make([]DomainData, 0, len(rows))
	for _, r := range rows {
		out = append(out, DomainData{Name: r.Name, MinimumTtl: r.MinimumTtl})
	}
	return out, nil
}

type ZoneData struct {
	Id   string `json:"id"`
	Name string `json:"name"`
}

func CloudflareZones(token string) ([]ZoneData, error) {
	body, err := fetchJson("https://api.cloudflare.com/client/v4/zones?per_page=50", "Bearer "+token)
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
		return nil, model.Errorf("cloudflare rejected the token: %s", strings.Join(msgs, "; "))
	}
	return env.Result, nil
}

func fetchJson(url, auth string) ([]byte, error) {
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

func ListeningPorts() []hostinfo.PortData {
	ports, _ := hostinfo.HostBackendFactory{}.Create().ListeningPorts()
	return ports
}
