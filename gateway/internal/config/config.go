// Package config loads the gateway's YAML configuration.
package config

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    Server              `yaml:"server"`
	Cache     Cache               `yaml:"cache"`
	Providers map[string]Provider `yaml:"providers"`
	Aliases   map[string][]Target `yaml:"aliases"`
}

type Server struct {
	Listen         string        `yaml:"listen"`
	MaxAttempts    int           `yaml:"max_attempts"`
	Cooldown       time.Duration `yaml:"cooldown"`
	AttemptTimeout time.Duration `yaml:"attempt_timeout"`
}

type Cache struct {
	Addr    string        `yaml:"addr"`
	Timeout time.Duration `yaml:"timeout"`
}

type Provider struct {
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
	Limits    Limits `yaml:"limits"`

	// APIKey is read from the environment named by APIKeyEnv; never from YAML.
	APIKey string `yaml:"-"`
}

// Limits are the provider's free-tier limits. Zero means "not configured".
// They are consumed by fl-guard; the gateway core does not enforce them.
type Limits struct {
	RequestsPerMinute int   `yaml:"requests_per_minute"`
	RequestsPerDay    int   `yaml:"requests_per_day"`
	TokensPerDay      int64 `yaml:"tokens_per_day"`
}

type Target struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// Load reads and validates the config at path. getenv is injectable for tests.
func Load(path string, getenv func(string) string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw, getenv)
}

func Parse(raw []byte, getenv func(string) string) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Server.Listen == "" {
		c.Server.Listen = ":8080"
	}
	if c.Server.MaxAttempts <= 0 {
		c.Server.MaxAttempts = 3
	}
	if c.Server.Cooldown <= 0 {
		c.Server.Cooldown = 30 * time.Second
	}
	if c.Server.AttemptTimeout <= 0 {
		c.Server.AttemptTimeout = 30 * time.Second
	}
	if v := getenv("FL_CACHE_ADDR"); v != "" {
		c.Cache.Addr = v
	}
	if c.Cache.Addr == "" {
		c.Cache.Addr = "localhost:6400"
	}
	if c.Cache.Timeout <= 0 {
		c.Cache.Timeout = 100 * time.Millisecond
	}

	if len(c.Providers) == 0 {
		return nil, fmt.Errorf("config: no providers defined")
	}
	for name, p := range c.Providers {
		u, err := url.Parse(p.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("config: provider %q has invalid base_url %q", name, p.BaseURL)
		}
		if p.APIKeyEnv != "" {
			p.APIKey = getenv(p.APIKeyEnv)
		}
		c.Providers[name] = p
	}
	if len(c.Aliases) == 0 {
		return nil, fmt.Errorf("config: no aliases defined")
	}
	for alias, targets := range c.Aliases {
		if len(targets) == 0 {
			return nil, fmt.Errorf("config: alias %q has no targets", alias)
		}
		for _, t := range targets {
			if _, ok := c.Providers[t.Provider]; !ok {
				return nil, fmt.Errorf("config: alias %q references unknown provider %q", alias, t.Provider)
			}
			if t.Model == "" {
				return nil, fmt.Errorf("config: alias %q has a target with no model", alias)
			}
		}
	}
	return &c, nil
}
