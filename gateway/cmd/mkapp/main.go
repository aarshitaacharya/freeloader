// Command mkapp creates an app and prints its API key once. Only the SHA-256 of
// the key is stored. It stands in for the admin panel until level 9.
//
//	DATABASE_URL=postgres://... go run ./cmd/mkapp --name demo --rpm 30
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/aarshitaacharya/freeloader/gateway/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	name := flag.String("name", "", "app name (required, unique)")
	rpm := flag.Int("rpm", 60, "requests per minute (0 = unlimited)")
	tokens := flag.Int64("tokens-per-day", 100000, "tokens per day (0 = unlimited)")
	ttl := flag.Int("cache-ttl", 86400, "response cache TTL in seconds (0 = no caching)")
	priority := flag.String("priority", "", "comma-separated provider order, e.g. groq,gemini (empty = alias default)")
	cacheAll := flag.Bool("cache-all", false, "cache requests whose temperature is not 0")
	flag.Parse()
	if *name == "" || os.Getenv("DATABASE_URL") == "" {
		fmt.Fprintln(os.Stderr, "usage: DATABASE_URL=... mkapp --name NAME [--rpm N] [--tokens-per-day N] [--cache-ttl S] [--priority a,b] [--cache-all]")
		os.Exit(2)
	}

	key, keyHash, err := auth.GenerateKey()
	if err != nil {
		fatal(err)
	}

	prio := []string{}
	if *priority != "" {
		prio = strings.Split(*priority, ",")
	}
	prioJSON, _ := json.Marshal(prio)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `
		INSERT INTO apps (name, key_hash, rpm_limit, tokens_per_day, cache_ttl_s, provider_priority, cache_all)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		*name, keyHash, *rpm, *tokens, *ttl, prioJSON, *cacheAll)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("app %q created. API key (shown once, not stored):\n%s\n", *name, key)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
