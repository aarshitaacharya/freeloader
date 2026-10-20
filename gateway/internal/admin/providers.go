package admin

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/cache"
	"github.com/aarshitaacharya/freeloader/gateway/internal/config"
	"github.com/aarshitaacharya/freeloader/gateway/internal/route"
)

type Limits struct {
	RequestsPerDay int64 `json:"requests_per_day"`
	TokensPerDay   int64 `json:"tokens_per_day"`
}

type ModelStatus struct {
	Model         string `json:"model"`
	RequestsToday int64  `json:"requests_today"`
	TokensToday   int64  `json:"tokens_today"`
	Limits        Limits `json:"limits"`
	Exhausted     bool   `json:"exhausted"`
}

type ProviderStatus struct {
	Name               string        `json:"name"`
	State              string        `json:"state"`
	CooldownRemainingS int           `json:"cooldown_remaining_s"`
	ResetTimezone      string        `json:"reset_timezone"`
	ResetsAt           time.Time     `json:"resets_at"`
	RequestsToday      int64         `json:"requests_today"`
	TokensToday        int64         `json:"tokens_today"`
	Limits             Limits        `json:"limits"`
	Models             []ModelStatus `json:"models"`
}

// Providers reads provider state from this replica's router and from the counters and
// flags fl-guard keeps in fl-cache (guard:req|tok:{scope}:{day}, exhausted:{scope}).
type Providers struct {
	Cfg    map[string]config.Provider
	Router *route.Router
	KV     cache.KV
}

func (p *Providers) Names() []string {
	out := make([]string, 0, len(p.Cfg))
	for n := range p.Cfg {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (p *Providers) Status(ctx context.Context, now time.Time) ([]ProviderStatus, error) {
	var out []ProviderStatus
	for _, name := range p.Names() {
		c := p.Cfg[name]
		loc := c.Loc
		if loc == nil {
			loc = time.UTC
		}
		local := now.In(loc)
		day := local.Format(time.DateOnly)
		y, m, d := local.Date()
		st := ProviderStatus{
			Name: name, ResetTimezone: loc.String(), Models: []ModelStatus{},
			ResetsAt: time.Date(y, m, d+1, 0, 0, 0, 0, loc),
			Limits:   Limits{RequestsPerDay: int64(c.Limits.RequestsPerDay), TokensPerDay: c.Limits.TokensPerDay},
		}

		counters := func(scope string) (req, tok int64, exhausted bool, err error) {
			if req, err = cache.GetInt(ctx, p.KV, fmt.Sprintf("guard:req:%s:%s", scope, day)); err != nil {
				return
			}
			if tok, err = cache.GetInt(ctx, p.KV, fmt.Sprintf("guard:tok:%s:%s", scope, day)); err != nil {
				return
			}
			_, exhausted, err = p.KV.Get(ctx, "exhausted:"+scope)
			return
		}

		req, tok, exhausted, err := counters(name)
		if err != nil {
			return nil, err
		}
		st.RequestsToday, st.TokensToday = req, tok

		models := make([]string, 0, len(c.Models))
		for model, lim := range c.Models {
			if lim.RequestsPerDay > 0 || lim.TokensPerDay > 0 { // only models the guard watches
				models = append(models, model)
			}
		}
		sort.Strings(models)
		for _, model := range models {
			lim := c.Models[model]
			mreq, mtok, mex, err := counters(name + "/" + model)
			if err != nil {
				return nil, err
			}
			st.Models = append(st.Models, ModelStatus{Model: model, RequestsToday: mreq, TokensToday: mtok,
				Limits: Limits{RequestsPerDay: int64(lim.RequestsPerDay), TokensPerDay: lim.TokensPerDay}, Exhausted: mex})
		}

		switch remaining := p.Router.CooldownRemaining([]config.Target{{Provider: name}}); {
		case c.APIKey == "":
			st.State = "no_api_key"
		case exhausted:
			st.State = "exhausted"
		case remaining > 0:
			st.State = "cooling_down"
			st.CooldownRemainingS = int((remaining + time.Second - 1) / time.Second)
		default:
			st.State = "healthy"
		}
		out = append(out, st)
	}
	return out, nil
}
