package singbox

import (
	"encoding/json"
	"testing"

	"useg/internal/store"
)

func TestBuildRoutesAndListenerPerCredential(t *testing.T) {
	in := Input{
		BoxIP:           "192.168.1.2",
		TProxyPort:      7895,
		ClashController: "127.0.0.1:9090",
		Persons: []store.Person{
			{ID: 1, Name: "ali", Enabled: true},
			{ID: 2, Name: "sara", Enabled: true},
		},
		Routes: []store.Route{
			{ID: 1, Name: "primary", Kind: "direct", Enabled: true},
			{ID: 2, Name: "psiphon", Kind: "socks", Enabled: true,
				UpstreamJSON: `{"type":"socks","server":"127.0.0.1","server_port":19000}`},
			{ID: 3, Name: "off", Kind: "direct", Enabled: false},
		},
		Credentials: []store.Credential{
			{PersonID: 1, RouteID: 1, UUID: "u1", Port: 10443, Enabled: true},
			{PersonID: 1, RouteID: 2, UUID: "u2", Port: 10444, Enabled: true},
			{PersonID: 2, RouteID: 1, UUID: "u3", Port: 10445, Enabled: true},
			{PersonID: 2, RouteID: 3, UUID: "u4", Port: 10446, Enabled: true}, // disabled route: no listener
		},
	}
	raw, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Inbounds []struct {
			Type       string `json:"type"`
			Tag        string `json:"tag"`
			ListenPort int    `json:"listen_port"`
			Listen     string `json:"listen"`
		} `json:"inbounds"`
		Outbounds []struct {
			Type   string `json:"type"`
			Tag    string `json:"tag"`
			Server string `json:"server"`
		} `json:"outbounds"`
		Route struct {
			Rules []struct {
				Inbound  []string `json:"inbound"`
				Outbound string   `json:"outbound"`
				Action   string   `json:"action"`
			} `json:"rules"`
			Final string `json:"final"`
		} `json:"route"`
		Experimental struct {
			ClashAPI struct {
				Controller string `json:"external_controller"`
			} `json:"clash_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}

	tags := map[string]int{}
	for _, ib := range cfg.Inbounds {
		tags[ib.Tag] = ib.ListenPort
		if ib.Type == "vless" && ib.Listen != "192.168.1.2" {
			t.Errorf("vless inbound %s listens on %q, want the LAN ip", ib.Tag, ib.Listen)
		}
	}
	if tags["tp"] != 7895 {
		t.Errorf("missing tproxy inbound: %v", tags)
	}
	for tag, port := range map[string]int{"p1:primary": 10443, "p1:psiphon": 10444, "p2:primary": 10445} {
		if tags[tag] != port {
			t.Errorf("inbound %s port = %d, want %d", tag, tags[tag], port)
		}
	}
	if _, ok := tags["p2:off"]; ok {
		t.Error("disabled route must not get a listener")
	}

	ob := map[string]string{}
	for _, o := range cfg.Outbounds {
		ob[o.Tag] = o.Type + " " + o.Server
	}
	if ob["direct"] != "direct " {
		t.Errorf("direct outbound: %q", ob["direct"])
	}
	if ob["r2"] != "socks 127.0.0.1" {
		t.Errorf("upstream socks outbound: %q", ob["r2"])
	}

	if cfg.Route.Final != "direct" {
		t.Errorf("final = %q, want direct (lane A always leaves direct)", cfg.Route.Final)
	}
	if cfg.Route.Rules[0].Action != "sniff" {
		t.Errorf("first rule must be sniff, got %q", cfg.Route.Rules[0].Action)
	}
	found := false
	for _, r := range cfg.Route.Rules {
		if len(r.Inbound) == 1 && r.Inbound[0] == "p1:psiphon" && r.Outbound == "r2" {
			found = true
		}
	}
	if !found {
		t.Error("lane B rule p1:psiphon -> r2 missing")
	}
	if cfg.Experimental.ClashAPI.Controller != "127.0.0.1:9090" {
		t.Error("clash api must be loopback-pinned")
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	in := Input{BoxIP: "10.0.0.1", TProxyPort: 7895, ClashController: "127.0.0.1:9090",
		Persons: []store.Person{{ID: 1, Enabled: true}},
		Routes:  []store.Route{{ID: 1, Name: "a", Enabled: true}, {ID: 2, Name: "b", Enabled: true}},
		Credentials: []store.Credential{
			{PersonID: 1, RouteID: 2, UUID: "x", Port: 1, Enabled: true},
			{PersonID: 1, RouteID: 1, UUID: "y", Port: 2, Enabled: true},
		}}
	a, _ := Build(in)
	b, _ := Build(in)
	if string(a) != string(b) {
		t.Fatal("config regeneration must be byte-stable, or sing-box restarts every tick")
	}
}
