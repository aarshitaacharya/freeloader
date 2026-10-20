package events

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

// The usage event is a contract shared with fl-ledger and fl-guard
// (api/usage-event.example.json). If this fails, update the file *and* the consumers.
func TestEventMatchesTheSharedContract(t *testing.T) {
	raw, err := os.ReadFile("../../../api/usage-event.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var e Event
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields() // a field in the contract the producer lacks
	if err := dec.Decode(&e); err != nil {
		t.Fatalf("contract file does not decode into Event: %v", err)
	}

	out, _ := json.Marshal(e)
	var got, want map[string]any
	_ = json.Unmarshal(out, &got)
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(keys(got), keys(want)) {
		t.Errorf("event fields differ from the contract:\n produced: %v\n contract: %v", keys(got), keys(want))
	}
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
