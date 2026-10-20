// Package route decides which providers to try, in what order.
package route

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/auth"
	"github.com/aarshitaacharya/freeloader/gateway/internal/cache"
	"github.com/aarshitaacharya/freeloader/gateway/internal/config"
	"github.com/aarshitaacharya/freeloader/gateway/internal/provider"
)

var ErrUnknownModel = errors.New("unknown model")

type Router struct {
	aliases   map[string][]config.Target
	providers map[string]*provider.Provider
	KV        cache.KV
	cooldown  time.Duration

	mu    sync.Mutex
	until map[string]time.Time // provider -> end of local cooldown
	Now   func() time.Time
}

func New(aliases map[string][]config.Target, providers map[string]*provider.Provider, kv cache.KV, cooldown time.Duration) *Router {
	return &Router{aliases: aliases, providers: providers, KV: kv, cooldown: cooldown,
		until: map[string]time.Time{}, Now: time.Now}
}

func (r *Router) Provider(name string) *provider.Provider { return r.providers[name] }

// Aliases lists the alias names, sorted, for GET /v1/models.
func (r *Router) Aliases() []string {
	out := make([]string, 0, len(r.aliases))
	for a := range r.aliases {
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}

// Resolve turns a requested model into an ordered list of targets.
// "fast" is an alias; "groq/openai/gpt-oss-20b" names a provider explicitly
// (the first path segment is the provider, the rest is that provider's model id).
func (r *Router) Resolve(app *auth.App, model string) (alias string, targets []config.Target, err error) {
	if ts, ok := r.aliases[model]; ok {
		return model, r.prioritise(app, ts), nil
	}
	if prov, rest, ok := strings.Cut(model, "/"); ok && rest != "" {
		if _, known := r.providers[prov]; known {
			return "direct", []config.Target{{Provider: prov, Model: rest}}, nil
		}
	}
	return "", nil, ErrUnknownModel
}

// prioritise applies the app's provider_priority: providers it lists come first,
// in its order; providers it omits are dropped. An empty list keeps the alias order.
func (r *Router) prioritise(app *auth.App, ts []config.Target) []config.Target {
	if len(app.ProviderPriority) == 0 {
		return ts
	}
	var out []config.Target
	for _, name := range app.ProviderPriority {
		for _, t := range ts {
			if t.Provider == name {
				out = append(out, t)
			}
		}
	}
	return out
}

// Skip reports whether a provider should not be tried right now, and why.
// A cache error is treated as "not flagged": we'd rather try a provider than
// refuse to serve because the cache is down.
func (r *Router) Skip(ctx context.Context, name string) (string, bool) {
	p := r.providers[name]
	if p == nil || !p.Enabled() {
		return "no_api_key", true
	}
	if r.CoolingDown(name) {
		return "cooldown", true
	}
	if _, flagged, err := r.KV.Get(ctx, "exhausted:"+name); err == nil && flagged {
		return "exhausted", true
	}
	return "", false
}

// StartCooldown stops this replica routing to the provider for the configured time.
func (r *Router) StartCooldown(name string) {
	r.mu.Lock()
	r.until[name] = r.Now().Add(r.cooldown)
	r.mu.Unlock()
}

func (r *Router) CoolingDown(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Now().Before(r.until[name])
}

// CooldownRemaining is the longest remaining cooldown across the targets, for Retry-After.
func (r *Router) CooldownRemaining(ts []config.Target) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	var longest time.Duration
	for _, t := range ts {
		if d := r.until[t.Provider].Sub(r.Now()); d > longest {
			longest = d
		}
	}
	return longest
}
