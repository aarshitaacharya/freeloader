// Package event is fl-ledger's view of a llm.usage record. The gateway produces it;
// the shape is pinned by api/usage-event.example.json (see event_test.go).
package event

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

type Event struct {
	RequestID        uuid.UUID `json:"request_id"`
	TS               time.Time `json:"ts"`
	AppID            int64     `json:"app_id"`
	Provider         string    `json:"provider"` // "" when no provider answered
	Model            string    `json:"model"`
	Alias            string    `json:"alias"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TokensEstimated  bool      `json:"tokens_estimated"`
	LatencyMS        int64     `json:"latency_ms"`
	Status           int       `json:"status"`
	CacheHit         bool      `json:"cache_hit"`
	Attempts         int       `json:"attempts"`
}

// Tokens is what the event counts toward usage: prompt plus completion.
func (e Event) Tokens() int64 { return e.PromptTokens + e.CompletionTokens }

// Day is the UTC calendar day the event belongs to (the usage_daily key).
func (e Event) Day() time.Time {
	t := e.TS.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

var ErrInvalid = errors.New("invalid usage event")

// Parse decodes and validates one record value. An invalid event can never become
// valid by retrying, so callers skip it (and count it) rather than block the partition.
func Parse(value []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(value, &e); err != nil {
		return Event{}, errors.Join(ErrInvalid, err)
	}
	switch {
	case e.RequestID == uuid.Nil:
		return Event{}, errors.Join(ErrInvalid, errors.New("missing request_id"))
	case e.TS.IsZero():
		return Event{}, errors.Join(ErrInvalid, errors.New("missing ts"))
	}
	return e, nil
}
