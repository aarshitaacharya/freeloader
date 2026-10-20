// Package config loads the gateway's YAML configuration.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    Server              `yaml:"server"`
	Cache     Cache               `yaml:"cache"`
	Events    Events              `yaml:"events"`
	Providers map[string]Provider `yaml:"providers"`
	Aliases   map[string][]Target `yaml:"aliases"`
}

type Server struct {
	Listen         string        `yaml:"listen"`
	MaxAttempts    int           `yaml:"max_attempts"`
	Cooldown       time.Duration `yaml:"cooldown"`
	AttemptTimeout time.Duration `yaml:"attempt_timeout"`
	// StreamIdleTimeout is the longest a stream may go silent before it is cut.
	StreamIdleTimeout time.Duration `yaml:"stream_idle_timeout"`
}

// Events configures the usage-event producer. With no brokers, events are disabled.
type Events struct {
	Brokers []string `yaml:"brokers"` // overridden by FL_KAFKA_BROKERS (comma-separated)
	Topic   string   `yaml:"topic"`
}

type Cache struct {
	Addr    string        `yaml:"addr"`
	Timeout time.Duration `yaml:"timeout"`
}

type Provider struct {
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
	Limits    Limits `yaml:"limits"`

	// Read by the admin API (provider status) and by fl-guard, which enforces them.
	Reset  Reset             `yaml:"reset"`
	Models map[string]Limits `yaml:"models"` // per-model limits

	Loc *time.Location `yaml:"-"` // from Reset.Timezone

	// APIKey is read from the environment named by APIKeyEnv; never from YAML.
	APIKey string `yaml:"-"`
}

// Reset says when the provider's daily quota rolls over.
type Reset struct {
	Timezone string `yaml:"timezone"` // IANA name; default UTC
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
	if c.Server.StreamIdleTimeout <= 0 {
		c.Server.StreamIdleTimeout = 60 * time.Second
	}
	if v := getenv("FL_KAFKA_BROKERS"); v != "" {
		c.Events.Brokers = nil
		for _, b := range strings.Split(v, ",") {
			if b = strings.TrimSpace(b); b != "" {
				c.Events.Brokers = append(c.Events.Brokers, b)
			}
		}
	}
	if c.Events.Topic == "" {
		c.Events.Topic = "llm.usage"
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
		tz := p.Reset.Timezone
		if tz == "" {
			tz = "UTC"
		}
		if p.Loc, err = time.LoadLocation(tz); err != nil {
			return nil, fmt.Errorf("config: provider %q has an invalid reset timezone %q: %w", name, tz, err)
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
