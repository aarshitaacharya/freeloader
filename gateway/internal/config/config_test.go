package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestExampleConfigLoads(t *testing.T) {
	c, err := Load("../../../config/providers.example.yaml", env(map[string]string{"GROQ_API_KEY": "gk"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers["groq"].APIKey != "gk" {
		t.Error("api key not read from env")
	}
	if c.Providers["gemini"].APIKey != "" {
		t.Error("unset env var should leave the key empty")
	}
	if c.Server.MaxAttempts != 3 || c.Server.Cooldown != 30*time.Second {
		t.Errorf("unexpected server settings: %+v", c.Server)
	}
	if len(c.Aliases["fast"]) != 3 {
		t.Errorf("fast alias: %+v", c.Aliases["fast"])
	}
	// Free-tier limits must come from config and only where verified; the example
	// must not invent Groq or Gemini numbers.
	if (c.Providers["groq"].Limits != Limits{}) || (c.Providers["gemini"].Limits != Limits{}) {
		t.Error("groq/gemini limits should be left unset in the example")
	}
	if c.Providers["openrouter"].Limits.RequestsPerDay != 50 {
		t.Error("openrouter limit missing")
	}
}

func TestExampleConfigHasNoSecrets(t *testing.T) {
	b, _ := os.ReadFile("../../../config/providers.example.yaml")
	for _, bad := range []string{"gsk_", "sk-or-", "AIza"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("example config contains something that looks like a key (%q)", bad)
		}
	}
}

func TestValidation(t *testing.T) {
	good := "providers:\n  a: {base_url: 'https://a.example/v1'}\naliases:\n  fast: [{provider: a, model: m}]\n"
	if _, err := Parse([]byte(good), env(nil)); err != nil {
		t.Fatalf("good config rejected: %v", err)
	}
	tests := []struct{ name, yaml, want string }{
		{"no providers", "aliases: {x: [{provider: a, model: m}]}", "no providers"},
		{"bad url", "providers: {a: {base_url: 'not a url'}}\naliases: {x: [{provider: a, model: m}]}", "invalid base_url"},
		{"no aliases", "providers: {a: {base_url: 'https://a/v1'}}", "no aliases"},
		{"unknown provider", "providers: {a: {base_url: 'https://a/v1'}}\naliases: {x: [{provider: b, model: m}]}", "unknown provider"},
		{"empty alias", "providers: {a: {base_url: 'https://a/v1'}}\naliases: {x: []}", "no targets"},
		{"no model", "providers: {a: {base_url: 'https://a/v1'}}\naliases: {x: [{provider: a}]}", "no model"},
		{"bad yaml", "providers: [", "parse config"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml), env(nil))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestCacheAddrEnvOverride(t *testing.T) {
	good := "providers:\n  a: {base_url: 'https://a.example/v1'}\naliases:\n  fast: [{provider: a, model: m}]\n"
	c, _ := Parse([]byte(good), env(map[string]string{"FL_CACHE_ADDR": "fl-cache:6400"}))
	if c.Cache.Addr != "fl-cache:6400" {
		t.Errorf("addr = %q", c.Cache.Addr)
	}
}
