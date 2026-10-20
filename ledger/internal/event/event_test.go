package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"
)

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The event is a contract shared with the gateway and fl-guard
// (api/usage-event.example.json). If this fails, update the file and all three.
func TestEventMatchesTheSharedContract(t *testing.T) {
	raw, err := os.ReadFile("../../../api/usage-event.example.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var e Event
	if err := dec.Decode(&e); err != nil {
		t.Fatalf("contract file does not decode into Event: %v", err)
	}
	out, _ := json.Marshal(e)
	var got, want map[string]any
	_ = json.Unmarshal(out, &got)
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(keysOf(got), keysOf(want)) {
		t.Errorf("fields differ from the contract:\n ledger:   %v\n contract: %v", keysOf(got), keysOf(want))
	}
	if _, err := Parse(raw); err != nil {
		t.Errorf("the contract example must parse: %v", err)
	}
}

func TestParseRejectsEventsThatCanNeverBeValid(t *testing.T) {
	for name, in := range map[string]string{
		"not json":       `{nope`,
		"not an object":  `[1]`,
		"no request id":  `{"ts":"2026-10-20T01:00:00Z","app_id":1}`,
		"bad request id": `{"request_id":"nope","ts":"2026-10-20T01:00:00Z"}`,
		"no ts":          `{"request_id":"01a15700-b914-7973-8b24-52324a8ae4e2"}`,
		"empty":          ``,
	} {
		if _, err := Parse([]byte(in)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestDayIsTheUTCDay(t *testing.T) {
	loc := time.FixedZone("UTC-8", -8*3600)
	e := Event{TS: time.Date(2026, 10, 19, 23, 30, 0, 0, loc)} // 07:30 UTC on the 20th
	if got := e.Day().Format("2006-01-02"); got != "2026-10-20" {
		t.Errorf("day = %s, want 2026-10-20", got)
	}
	if (Event{PromptTokens: 3, CompletionTokens: 4}).Tokens() != 7 {
		t.Error("Tokens() must be prompt + completion")
	}
}
