// Package config reads the guard's view of config/providers.yaml: each provider's
// daily limits (provider-wide and per model) and when its day resets.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Guard     Guard               `yaml:"guard"`
	Providers map[string]Provider `yaml:"providers"`
}

type Guard struct {
	// Threshold is the fraction of a daily limit at which a provider/model is flagged exhausted.
	Threshold float64 `yaml:"threshold"`
}

type Provider struct {
	Reset  Reset             `yaml:"reset"`
	Limits Limits            `yaml:"limits"` // shared by all of the provider's models
	Models map[string]Limits `yaml:"models"` // per-model limits

	Loc *time.Location `yaml:"-"`
}

type Reset struct {
	Timezone string `yaml:"timezone"` // IANA name; default UTC
}

// Limits: zero means "not configured". Only daily limits are guarded.
type Limits struct {
	RequestsPerMinute int   `yaml:"requests_per_minute"`
	RequestsPerDay    int   `yaml:"requests_per_day"`
	TokensPerDay      int64 `yaml:"tokens_per_day"`
}

// Enforced reports whether the guard has anything to check for these limits.
func (l Limits) Enforced() bool { return l.RequestsPerDay > 0 || l.TokensPerDay > 0 }

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Guard.Threshold == 0 {
		c.Guard.Threshold = 0.9
	}
	if c.Guard.Threshold < 0 || c.Guard.Threshold > 1 {
		return nil, fmt.Errorf("guard.threshold must be between 0 and 1, got %v", c.Guard.Threshold)
	}
	if len(c.Providers) == 0 {
		return nil, fmt.Errorf("config: no providers defined")
	}
	for name, p := range c.Providers {
		tz := p.Reset.Timezone
		if tz == "" {
			tz = "UTC"
		}
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("config: provider %q has an invalid reset timezone %q: %w", name, tz, err)
		}
		p.Loc = loc
		c.Providers[name] = p
	}
	return &c, nil
}
