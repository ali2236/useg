// Package singbox generates the data plane config, supervises the process and
// reads its Clash API. The generated config has no domain routing rules: routes
// map one-to-one to inbounds, and domain classes exist only for rating.
package singbox

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"

	"useg/internal/store"
)

type Input struct {
	BoxIP           string
	TProxyPort      int
	ClashController string // host:port
	Routes          []store.Route
	Credentials     []store.Credential
	Persons         []store.Person
}

func InboundTag(personID int64, routeName string) string {
	return fmt.Sprintf("p%d:%s", personID, routeName)
}

func OutboundTag(routeID int64) string { return fmt.Sprintf("r%d", routeID) }

type sbConfig struct {
	Log          map[string]any   `json:"log"`
	Inbounds     []map[string]any `json:"inbounds"`
	Outbounds    []map[string]any `json:"outbounds"`
	Route        sbRoute          `json:"route"`
	Experimental map[string]any   `json:"experimental"`
}

type sbRoute struct {
	Rules []map[string]any `json:"rules"`
	Final string           `json:"final"`
}

// Build renders the whole data plane. Rebuilt only when routes or credentials
// change; the supervisor restarts the process only if the bytes differ.
func Build(in Input) ([]byte, error) {
	personByID := map[int64]store.Person{}
	for _, p := range in.Persons {
		personByID[p.ID] = p
	}
	routeByID := map[int64]store.Route{}
	for _, r := range in.Routes {
		routeByID[r.ID] = r
	}

	routes := append([]store.Route(nil), in.Routes...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].ID < routes[j].ID })

	outbounds := []map[string]any{{"type": "direct", "tag": "direct"}}
	for _, r := range routes {
		if !r.Enabled {
			continue
		}
		ob := map[string]any{}
		if r.UpstreamJSON != "" && r.UpstreamJSON != "{}" {
			if err := json.Unmarshal([]byte(r.UpstreamJSON), &ob); err != nil {
				return nil, fmt.Errorf("route %q upstream_json: %w", r.Name, err)
			}
		}
		if _, ok := ob["type"]; !ok {
			ob["type"] = "direct"
		}
		ob["tag"] = OutboundTag(r.ID)
		outbounds = append(outbounds, ob)
	}

	inbounds := []map[string]any{{
		"type":        "tproxy",
		"tag":         "tp",
		"listen":      "0.0.0.0",
		"listen_port": in.TProxyPort,
	}}

	creds := append([]store.Credential(nil), in.Credentials...)
	sort.Slice(creds, func(i, j int) bool {
		if creds[i].PersonID != creds[j].PersonID {
			return creds[i].PersonID < creds[j].PersonID
		}
		return creds[i].RouteID < creds[j].RouteID
	})

	rules := []map[string]any{{"action": "sniff"}}
	for _, c := range creds {
		person, okP := personByID[c.PersonID]
		route, okR := routeByID[c.RouteID]
		if !okP || !okR || !c.Enabled || !person.Enabled || !route.Enabled {
			continue
		}
		tag := InboundTag(person.ID, route.Name)
		inbounds = append(inbounds, map[string]any{
			"type":        "vless",
			"tag":         tag,
			"listen":      in.BoxIP,
			"listen_port": c.Port,
			"users": []map[string]any{{
				"name": fmt.Sprintf("p%d", person.ID),
				"uuid": c.UUID,
			}},
		})
		rules = append(rules, map[string]any{
			"inbound":  []string{tag},
			"outbound": OutboundTag(route.ID),
		})
	}

	cfg := sbConfig{
		Log:       map[string]any{"level": "warn", "timestamp": true},
		Inbounds:  inbounds,
		Outbounds: outbounds,
		Route:     sbRoute{Rules: rules, Final: "direct"},
		Experimental: map[string]any{
			"clash_api":  map[string]any{"external_controller": in.ClashController},
			"cache_file": map[string]any{"enabled": false},
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func ClashController(clashURL string) string {
	u, err := url.Parse(clashURL)
	if err != nil || u.Host == "" {
		return "127.0.0.1:9090"
	}
	return u.Host
}
