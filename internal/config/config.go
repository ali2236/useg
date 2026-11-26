package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
	_ "time/tzdata" // the Debian box must not depend on /usr/share/zoneinfo
)

type Config struct {
	DataDir       string `yaml:"data_dir"`
	DBPath        string `yaml:"db"`
	LanCIDR       string `yaml:"lan_cidr"`
	BoxIP         string `yaml:"box_ip"`
	RouterIP      string `yaml:"router_ip"`
	Iface         string `yaml:"iface"`
	PortalListen  string `yaml:"portal_listen"`
	AdminListen   string `yaml:"admin_listen"`
	AdminPassword string `yaml:"admin_password"`
	PortalPort    int    `yaml:"portal_port"` // redirect target for blocked HTTP
	TProxyPort    int    `yaml:"tproxy_port"`
	ClashURL      string `yaml:"clash_url"`
	SingboxBin    string `yaml:"singbox_bin"`
	SingboxConfig string `yaml:"singbox_config"`
	LeasesFile    string `yaml:"leases_file"`
	Timezone      string `yaml:"timezone"`
	PollMS        int    `yaml:"poll_ms"` // /connections poll; 250 ms, not 1 Hz
	Enforce       bool   `yaml:"enforce"` // false on a dev Mac, true on the box
	Dev           bool   `yaml:"dev"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.defaults()
	if c.LanCIDR == "" || c.BoxIP == "" || c.RouterIP == "" {
		return nil, errors.New("config: lan_cidr, box_ip and router_ip are required")
	}
	return c, nil
}

func (c *Config) defaults() {
	if c.DataDir == "" {
		c.DataDir = "/var/lib/useg"
	}
	if c.DBPath == "" {
		c.DBPath = c.DataDir + "/useg.db"
	}
	if c.SingboxConfig == "" {
		c.SingboxConfig = c.DataDir + "/sing-box.json"
	}
	if c.Iface == "" {
		c.Iface = "eth0"
	}
	if c.PortalListen == "" {
		c.PortalListen = ":80"
	}
	if c.AdminListen == "" {
		c.AdminListen = ":8080"
	}
	if c.PortalPort == 0 {
		c.PortalPort = 80
	}
	if c.TProxyPort == 0 {
		c.TProxyPort = 7895
	}
	if c.ClashURL == "" {
		c.ClashURL = "http://127.0.0.1:9090"
	}
	if c.SingboxBin == "" {
		c.SingboxBin = "sing-box"
	}
	if c.LeasesFile == "" {
		c.LeasesFile = "/var/lib/misc/dnsmasq.leases"
	}
	if c.Timezone == "" {
		c.Timezone = "Asia/Tehran"
	}
	// The design says 1 Hz, but /connections only lists open connections, so a
	// 1 s poll loses whole short flows, not just their tail. 250 ms keeps most
	// page-load connections visible at negligible cost.
	if c.PollMS < 50 {
		c.PollMS = 250
	}
}

func (c *Config) Location() *time.Location {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		loc = time.FixedZone("+0330", 3*3600+30*60)
	}
	return loc
}

func (c *Config) EnsureDirs() error { return os.MkdirAll(c.DataDir, 0o750) }
