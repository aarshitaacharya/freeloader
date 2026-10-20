// Package admin is the JSON API behind the control panel (/admin/*). The contract is
// api/openapi.yaml; admin_test.go checks real responses against it.
package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/auth"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrNameTaken = errors.New("name taken")
)

// App mirrors the OpenAPI App schema.
type App struct {
	ID               int64     `json:"id"`
	Name             string    `json:"name"`
	RPMLimit         int       `json:"rpm_limit"`
	TokensPerDay     int64     `json:"tokens_per_day"`
	CacheTTLSeconds  int       `json:"cache_ttl_s"`
	ProviderPriority []string  `json:"provider_priority"`
	CacheAll         bool      `json:"cache_all"`
	Disabled         bool      `json:"disabled"`
	CreatedAt        time.Time `json:"created_at"`
}

type NewApp struct {
	Name             string   `json:"name"`
	RPMLimit         *int     `json:"rpm_limit"`
	TokensPerDay     *int64   `json:"tokens_per_day"`
	CacheTTLSeconds  *int     `json:"cache_ttl_s"`
	ProviderPriority []string `json:"provider_priority"`
	CacheAll         *bool    `json:"cache_all"`
}

// AppPatch: nil fields are left unchanged.
type AppPatch struct {
	RPMLimit         *int      `json:"rpm_limit"`
	TokensPerDay     *int64    `json:"tokens_per_day"`
	CacheTTLSeconds  *int      `json:"cache_ttl_s"`
	ProviderPriority *[]string `json:"provider_priority"`
	CacheAll         *bool     `json:"cache_all"`
	Disabled         *bool     `json:"disabled"`
}

type UsageRow struct {
	Day       string `json:"day"`
	AppID     int64  `json:"app_id"`
	AppName   string `json:"app_name"`
	Provider  string `json:"provider"`
	Requests  int64  `json:"requests"`
	Tokens    int64  `json:"tokens"`
	CacheHits int64  `json:"cache_hits"`
}

// Store is the Postgres side of the API.
type Store interface {
	ListApps(ctx context.Context) ([]App, error)
	CreateApp(ctx context.Context, in NewApp, keyHash []byte) (App, error) // ErrNameTaken
	UpdateApp(ctx context.Context, id int64, p AppPatch) (App, error)      // ErrNotFound
	Usage(ctx context.Context, from, to time.Time, appID *int64) ([]UsageRow, error)
}

type Handler struct {
	token     [sha256.Size]byte
	store     Store
	providers *Providers
	known     map[string]bool // provider names valid in provider_priority
	log       *slog.Logger
	now       func() time.Time
}

// New returns the handler for /admin/*. token must not be empty.
func New(token string, store Store, providers *Providers, log *slog.Logger) (*Handler, error) {
	if token == "" {
		return nil, errors.New("admin: empty token")
	}
	if log == nil {
		log = slog.Default()
	}
	known := map[string]bool{}
	for _, n := range providers.Names() {
		known[n] = true
	}
	return &Handler{token: sha256.Sum256([]byte(token)), store: store, providers: providers,
		known: known, log: log, now: time.Now}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/apps", h.listApps)
	mux.HandleFunc("POST /admin/apps", h.createApp)
	mux.HandleFunc("PATCH /admin/apps/{id}", h.updateApp)
	mux.HandleFunc("GET /admin/usage", h.usage)
	mux.HandleFunc("GET /admin/providers", h.listProviders)
	if !h.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="freeloader-admin"`)
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or wrong admin token")
		return
	}
	mux.ServeHTTP(w, r)
}

func (h *Handler) authorized(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	got := sha256.Sum256([]byte(strings.TrimSpace(tok)))
	return subtle.ConstantTimeCompare(got[:], h.token[:]) == 1
}

func (h *Handler) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.store.ListApps(r.Context())
	if err != nil {
		h.internal(w, "list apps", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

func (h *Handler) createApp(w http.ResponseWriter, r *http.Request) {
	var in NewApp
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_request", "name must be 1 to 64 characters")
		return
	}
	// Defaults match the OpenAPI schema and cmd/mkapp.
	if in.RPMLimit == nil {
		in.RPMLimit = ptr(60)
	}
	if in.TokensPerDay == nil {
		in.TokensPerDay = ptr(int64(100000))
	}
	if in.CacheTTLSeconds == nil {
		in.CacheTTLSeconds = ptr(86400)
	}
	if in.CacheAll == nil {
		in.CacheAll = ptr(false)
	}
	if in.ProviderPriority == nil {
		in.ProviderPriority = []string{}
	}
	if msg := h.validate(*in.RPMLimit, *in.TokensPerDay, *in.CacheTTLSeconds, in.ProviderPriority); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	key, hash, err := auth.GenerateKey()
	if err != nil {
		h.internal(w, "generate key", err)
		return
	}
	app, err := h.store.CreateApp(r.Context(), in, hash)
	switch {
	case errors.Is(err, ErrNameTaken):
		writeError(w, http.StatusConflict, "name_taken", "an app with that name already exists")
	case err != nil:
		h.internal(w, "create app", err)
	default:
		h.log.Info("app created", "app_id", app.ID, "name", app.Name)
		writeJSON(w, http.StatusCreated, map[string]any{"app": app, "key": key})
	}
}

func (h *Handler) updateApp(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "id must be a positive integer")
		return
	}
	var p AppPatch
	if !decode(w, r, &p) {
		return
	}
	rpm, tok, ttl, prio := 0, int64(0), 0, []string{}
	if p.RPMLimit != nil {
		rpm = *p.RPMLimit
	}
	if p.TokensPerDay != nil {
		tok = *p.TokensPerDay
	}
	if p.CacheTTLSeconds != nil {
		ttl = *p.CacheTTLSeconds
	}
	if p.ProviderPriority != nil {
		prio = *p.ProviderPriority
	}
	if msg := h.validate(rpm, tok, ttl, prio); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	app, err := h.store.UpdateApp(r.Context(), id, p)
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such app")
	case err != nil:
		h.internal(w, "update app", err)
	default:
		h.log.Info("app updated", "app_id", app.ID, "disabled", app.Disabled)
		writeJSON(w, http.StatusOK, app)
	}
}

func (h *Handler) validate(rpm int, tokens int64, ttl int, priority []string) string {
	switch {
	case rpm < 0:
		return "rpm_limit must be 0 or more"
	case tokens < 0:
		return "tokens_per_day must be 0 or more"
	case ttl < 0:
		return "cache_ttl_s must be 0 or more"
	}
	seen := map[string]bool{}
	for _, name := range priority {
		if !h.known[name] {
			return "provider_priority: unknown provider " + strconv.Quote(name)
		}
		if seen[name] {
			return "provider_priority: " + strconv.Quote(name) + " is listed twice"
		}
		seen[name] = true
	}
	return ""
}

func (h *Handler) usage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	today := h.now().UTC().Truncate(24 * time.Hour)
	to, from := today, time.Time{}
	var err error
	if v := q.Get("to"); v != "" {
		if to, err = time.Parse(time.DateOnly, v); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "to must be YYYY-MM-DD")
			return
		}
	}
	if v := q.Get("from"); v != "" {
		if from, err = time.Parse(time.DateOnly, v); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "from must be YYYY-MM-DD")
			return
		}
	} else {
		from = to.AddDate(0, 0, -6)
	}
	if from.After(to) {
		writeError(w, http.StatusBadRequest, "invalid_request", "from is after to")
		return
	}
	if to.Sub(from) > 366*24*time.Hour {
		writeError(w, http.StatusBadRequest, "invalid_request", "range is limited to 366 days")
		return
	}
	var appID *int64
	if v := q.Get("app_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "app_id must be an integer")
			return
		}
		appID = &id
	}
	rows, err := h.store.Usage(r.Context(), from, to, appID)
	if err != nil {
		h.internal(w, "usage", err)
		return
	}
	if rows == nil {
		rows = []UsageRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly), "rows": rows})
}

func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) {
	out, err := h.providers.Status(r.Context(), h.now())
	if err != nil {
		h.log.Error("provider status", "err", err)
		writeError(w, http.StatusServiceUnavailable, "cache_unavailable", "cannot read quota counters from fl-cache")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

func (h *Handler) internal(w http.ResponseWriter, what string, err error) {
	h.log.Error("admin: "+what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

func ptr[T any](v T) *T { return &v }

const maxBody = 64 << 10

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
