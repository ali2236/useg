package singbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Client reads the Clash API. It is bound to loopback everywhere: it can close
// connections and read metadata.
type Client struct {
	base string
	hc   *http.Client
}

func NewClient(base string) *Client {
	return &Client{base: base, hc: &http.Client{Timeout: 3 * time.Second}}
}

// flexString accepts the string-or-number encoding Clash uses for ports.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(b, `"`)
	if string(b) == "null" {
		return nil
	}
	*f = flexString(b)
	return nil
}

func (f flexString) Int() int {
	n, _ := strconv.Atoi(string(f))
	return n
}

type Metadata struct {
	Network         string     `json:"network"`
	Type            string     `json:"type"`
	SourceIP        string     `json:"sourceIP"`
	DestinationIP   string     `json:"destinationIP"`
	SourcePort      flexString `json:"sourcePort"`
	DestinationPort flexString `json:"destinationPort"`
	Host            string     `json:"host"`
	SniffHost       string     `json:"sniffHost"`
	InboundName     string     `json:"inboundName"`
	InboundUser     string     `json:"inboundUser"`
}

type Conn struct {
	ID       string   `json:"id"`
	Upload   int64    `json:"upload"`
	Download int64    `json:"download"`
	Chains   []string `json:"chains"`
	Rule     string   `json:"rule"`
	Metadata Metadata `json:"metadata"`
}

type connectionsResp struct {
	Connections []Conn `json:"connections"`
}

func (c *Client) Connections() ([]Conn, error) {
	resp, err := c.hc.Get(c.base + "/connections")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("clash api /connections: %s", resp.Status)
	}
	var out connectionsResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Connections, nil
}

// Probe returns the raw JSON key set, used by `useg probe` to answer the P0
// question about which metadata fields this sing-box version really exposes.
func (c *Client) Probe() ([]byte, error) {
	resp, err := c.hc.Get(c.base + "/connections")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if conns, ok := raw["connections"].([]any); ok && len(conns) > 0 {
		raw["connections"] = []any{conns[0]}
	}
	return json.MarshalIndent(raw, "", "  ")
}
