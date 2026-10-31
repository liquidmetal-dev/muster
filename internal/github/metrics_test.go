package github_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/github"
)

func TestCounters(t *testing.T) {
	var c github.Counters
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			c.ObserveCall("GET /user", 200)
			c.ObserveError("GET /user", "server")
		})
	}
	wg.Wait()
	if got := c.Calls("GET /user"); got != 10 {
		t.Fatalf("Calls = %d, want 10", got)
	}
	if got := c.Errors("GET /user", "server"); got != 10 {
		t.Fatalf("Errors = %d, want 10", got)
	}
	if got := c.Errors("GET /user", "network"); got != 0 {
		t.Fatalf("Errors(network) = %d, want 0", got)
	}
}

func TestRateLimitedErrorIs(t *testing.T) {
	var err error = &github.RateLimitedError{Endpoint: "GET /user"}
	if !errors.Is(err, github.ErrRateLimited) {
		t.Fatal("RateLimitedError does not match ErrRateLimited")
	}
}
