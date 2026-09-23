package client

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Config is an agent's saved connection to its relay.
type Config struct {
	Relay string `json:"relay"`           // relay base URL, e.g. http://tincan-relay
	Proxy string `json:"proxy,omitempty"` // proxy for relay traffic (Muse: its tailnet tunnel proxy)
	Agent string `json:"agent,omitempty"` // the name this machine joined as
}

// ConfigPath is where the agent config lives.
func ConfigPath() string {
	if p := os.Getenv("TINCAN_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "tincan", "client.json")
}

// LoadConfig reads the saved config, then applies TINCAN_RELAY and
// TINCAN_PROXY overrides.
func LoadConfig() (Config, error) {
	var c Config
	raw, err := os.ReadFile(ConfigPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return c, err
	default:
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, err
		}
	}
	if v := os.Getenv("TINCAN_RELAY"); v != "" {
		c.Relay = v
	}
	if v := os.Getenv("TINCAN_PROXY"); v != "" {
		c.Proxy = v
	}
	return c, nil
}

// SaveConfig writes the config with owner-only permissions.
func SaveConfig(c Config) error {
	p := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
