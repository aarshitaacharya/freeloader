package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	apps  map[string]*App // by hex of hash is overkill; key by raw key string via HashKey compare
	calls int
	err   error
}

func (f *fakeStore) LookupByKeyHash(_ context.Context, hash []byte) (*App, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	for k, a := range f.apps {
		if string(HashKey(k)) == string(hash) {
			cp := *a
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}
func (f *fakeStore) Ping(context.Context) error { return nil }

func newAuth(s *fakeStore) (*Authenticator, *time.Time) {
	now := time.Unix(1_000_000, 0)
	a := New(s, 30*time.Second)
	a.now = func() time.Time { return now }
	return a, &now
}

func TestAuthenticate(t *testing.T) {
	store := &fakeStore{apps: map[string]*App{
		"fl_good":     {ID: 1, Name: "good"},
		"fl_disabled": {ID: 2, Name: "off", Disabled: true},
	}}
	a, _ := newAuth(store)
	ctx := context.Background()

	tests := []struct {
		name, header string
		want         error
		appName      string
	}{
		{"ok", "Bearer fl_good", nil, "good"},
		{"missing", "", ErrMissingKey, ""},
		{"wrong scheme", "Basic fl_good", ErrMissingKey, ""},
		{"empty token", "Bearer ", ErrMissingKey, ""},
		{"not our prefix", "Bearer sk-abc", ErrInvalidKey, ""},
		{"unknown key", "Bearer fl_nope", ErrInvalidKey, ""},
		{"disabled", "Bearer fl_disabled", ErrDisabled, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, err := a.Authenticate(ctx, tc.header)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.want == nil && app.Name != tc.appName {
				t.Errorf("app = %q", app.Name)
			}
		})
	}
}

func TestPrefixAndMissingNeverHitTheDatabase(t *testing.T) {
	store := &fakeStore{}
	a, _ := newAuth(store)
	for _, h := range []string{"", "Bearer ", "Bearer sk-abc"} {
		_, _ = a.Authenticate(context.Background(), h)
	}
	if store.calls != 0 {
		t.Errorf("db calls = %d, want 0", store.calls)
	}
}

func TestLookupsAreCachedFor30Seconds(t *testing.T) {
	store := &fakeStore{apps: map[string]*App{"fl_a": {ID: 1, Name: "a"}}}
	a, now := newAuth(store)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := a.Authenticate(ctx, "Bearer fl_a"); err != nil {
			t.Fatal(err)
		}
	}
	if store.calls != 1 {
		t.Fatalf("calls = %d, want 1 (cached)", store.calls)
	}
	*now = now.Add(29 * time.Second)
	_, _ = a.Authenticate(ctx, "Bearer fl_a")
	if store.calls != 1 {
		t.Fatalf("calls = %d after 29s, want 1", store.calls)
	}
	*now = now.Add(2 * time.Second)
	_, _ = a.Authenticate(ctx, "Bearer fl_a")
	if store.calls != 2 {
		t.Fatalf("calls = %d after 31s, want 2 (expired)", store.calls)
	}
}

func TestDisablingTakesEffectAfterTTL(t *testing.T) {
	store := &fakeStore{apps: map[string]*App{"fl_a": {ID: 1, Name: "a"}}}
	a, now := newAuth(store)
	ctx := context.Background()
	if _, err := a.Authenticate(ctx, "Bearer fl_a"); err != nil {
		t.Fatal(err)
	}
	store.apps["fl_a"].Disabled = true
	if _, err := a.Authenticate(ctx, "Bearer fl_a"); err != nil {
		t.Fatal("within the TTL the cached (enabled) app is still served")
	}
	*now = now.Add(31 * time.Second)
	if _, err := a.Authenticate(ctx, "Bearer fl_a"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("after TTL err = %v, want ErrDisabled", err)
	}
}

func TestBadKeysAreNegativelyCached(t *testing.T) {
	store := &fakeStore{apps: map[string]*App{}}
	a, _ := newAuth(store)
	for i := 0; i < 10; i++ {
		_, _ = a.Authenticate(context.Background(), "Bearer fl_guess")
	}
	if store.calls != 1 {
		t.Errorf("calls = %d, want 1", store.calls)
	}
}

func TestDatabaseErrorsAreNotCached(t *testing.T) {
	store := &fakeStore{err: errors.New("db down"), apps: map[string]*App{"fl_a": {ID: 1, Name: "a"}}}
	a, _ := newAuth(store)
	ctx := context.Background()
	if _, err := a.Authenticate(ctx, "Bearer fl_a"); err == nil || errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err = %v, want a backend error", err)
	}
	store.err = nil
	if _, err := a.Authenticate(ctx, "Bearer fl_a"); err != nil {
		t.Fatalf("recovered db should authenticate, got %v", err)
	}
}

func TestHashKeyIsSHA256(t *testing.T) {
	// SHA-256("abc")
	got := HashKey("abc")
	if len(got) != 32 || got[0] != 0xba || got[1] != 0x78 {
		t.Errorf("unexpected hash %x", got)
	}
}
