package github

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
)

// refreshMargin is how long before expiry an installation token is replaced.
const refreshMargin = 5 * time.Minute

type tokenSource interface {
	Token(ctx context.Context) (string, error)
	// Invalidate drops token if it is still the cached one, so the next
	// Token call fetches a new one.
	Invalidate(token string)
}

type patSource struct{ token string }

func (p patSource) Token(context.Context) (string, error) { return p.token, nil }
func (patSource) Invalidate(string)                       {}

// exchangeFunc trades the App JWT for an installation token.
type exchangeFunc func(ctx context.Context) (token string, expires time.Time, err error)

type appSource struct {
	clock    clock.Clock
	exchange exchangeFunc

	// mu is held across the exchange so concurrent callers share one refresh.
	mu  sync.Mutex
	tok string
	exp time.Time
}

func (a *appSource) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now()
	if a.tok != "" && now.Before(a.exp.Add(-refreshMargin)) {
		return a.tok, nil
	}
	tok, exp, err := a.exchange(ctx)
	if err != nil {
		if a.tok != "" && now.Before(a.exp) {
			return a.tok, nil
		}
		return "", fmt.Errorf("github: installation token: %w", err)
	}
	a.tok, a.exp = tok, exp
	return tok, nil
}

func (a *appSource) Invalidate(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tok == token {
		a.tok, a.exp = "", time.Time{}
	}
}
