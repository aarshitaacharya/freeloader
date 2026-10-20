// Command loadgen sends a steady stream of chat requests and reports every failure.
// Level 7 uses it to prove that deleting a gateway pod loses no requests.
//
//	loadgen --url http://127.0.0.1:8090 --key flk_... --duration 60s --rps 50
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8090", "base URL of the gateway or ingress")
	key := flag.String("key", os.Getenv("FL_KEY"), "API key (or FL_KEY)")
	model := flag.String("model", "fast", "model alias to request")
	dur := flag.Duration("duration", 60*time.Second, "how long to send")
	rps := flag.Int("rps", 50, "requests per second")
	flag.Parse()
	if *key == "" {
		fmt.Fprintln(os.Stderr, "need --key or FL_KEY")
		os.Exit(2)
	}

	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 64}}
	var (
		sent, ok atomic.Int64
		mu       sync.Mutex
		failures = map[string]int{}
		wg       sync.WaitGroup
	)
	fail := func(why string) { mu.Lock(); failures[why]++; mu.Unlock() }

	start := time.Now()
	tick := time.NewTicker(time.Second / time.Duration(*rps))
	defer tick.Stop()
	for i := 0; time.Since(start) < *dur; i++ {
		<-tick.C
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sent.Add(1)
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"load %d"}]}`, *model, n)
			req, _ := http.NewRequest(http.MethodPost, *url+"/v1/chat/completions", bytes.NewBufferString(body))
			req.Header.Set("Authorization", "Bearer "+*key)
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				fail("transport: " + err.Error())
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				fail(fmt.Sprintf("HTTP %d", resp.StatusCode))
				return
			}
			ok.Add(1)
		}(i)
	}
	wg.Wait()

	failed := sent.Load() - ok.Load()
	fmt.Printf("sent=%d ok=%d failed=%d over %s\n", sent.Load(), ok.Load(), failed, time.Since(start).Round(time.Second))
	keys := make([]string, 0, len(failures))
	for k := range failures {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %5d  %s\n", failures[k], k)
	}
	if failed > 0 {
		os.Exit(1)
	}
}
