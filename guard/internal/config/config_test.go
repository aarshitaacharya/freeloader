package config

import (
	"strings"
	"testing"
)

func TestExampleConfigCarriesTheVerifiedLimits(t *testing.T) {
	c, err := Load("../../../config/providers.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Guard.Threshold != 0.9 {
		t.Errorf("threshold = %v, want 0.9", c.Guard.Threshold)
	}
	// Numbers copied from the providers' own pages; never invented. See the comments in the file.
	if l := c.Providers["groq"].Models["openai/gpt-oss-20b"]; l.RequestsPerDay != 1000 || l.TokensPerDay != 200000 || l.RequestsPerMinute != 30 {
		t.Errorf("groq gpt-oss-20b limits = %+v", l)
	}
	if l := c.Providers["gemini"].Models["gemini-3.1-flash-lite"]; l.RequestsPerDay != 500 || l.RequestsPerMinute != 15 {
		t.Errorf("gemini 3.1 flash lite limits = %+v", l)
	}
	if l := c.Providers["gemini"].Models["gemini-3.8-flash"]; l.RequestsPerDay != 20 {
		t.Errorf("gemini 3.8 flash limits = %+v", l)
	}
	if l := c.Providers["openrouter"].Limits; l.RequestsPerDay != 50 || l.RequestsPerMinute != 20 {
		t.Errorf("openrouter limits = %+v (a quota shared by every :free model)", l)
	}
	if got := c.Providers["gemini"].Loc.String(); got != "America/Los_Angeles" {
		t.Errorf("gemini resets in %q, want America/Los_Angeles (documented: midnight Pacific)", got)
	}
	if got := c.Providers["groq"].Loc.String(); got != "UTC" {
		t.Errorf("groq timezone = %q", got)
	}
}

func TestValidation(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"no providers":  {"guard: {threshold: 0.5}", "no providers"},
		"bad timezone":  {"providers: {a: {reset: {timezone: Mars/Olympus}}}", "invalid reset timezone"},
		"bad threshold": {"guard: {threshold: 1.5}\nproviders: {a: {}}", "threshold"},
		"bad yaml":      {"providers: [", "parse config"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.yaml)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
	c, err := Parse([]byte("providers: {a: {}}"))
	if err != nil || c.Guard.Threshold != 0.9 || c.Providers["a"].Loc.String() != "UTC" {
		t.Errorf("defaults: %+v %v", c, err)
	}
}
