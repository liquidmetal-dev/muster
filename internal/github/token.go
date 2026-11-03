package github

import (
	"context"
	"fmt"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
)

// refreshMargin is how long before expiry an installation token is replaced.
const refreshMargin = 5 * time.Minute

type tokenSource interface {
	Token(ctx context.Context) (string, error)
	// Invalidate drops token if it is still the cached one, so the next
	// Token call fetches a new one.
	Invalidate(ctx context.Context, token string)
}

type patSource struct{ token string }

func (p patSource) Token(context.Context) (string, error) { return p.token, nil }
func (patSource) Invalidate(context.Context, string)      {}

// exchangeFunc trades the App JWT for an installation token.
type exchangeFunc func(ctx context.Context) (token string, expires time.Time, err error)

type appSource struct {
	clock    clock.Clock
	exchange exchangeFunc

	// sem is a one-slot lock held across the exchange, so concurrent callers
	// share one refresh; unlike a mutex, a waiter can give up when its
	// context ends. It guards tok and exp.
	sem chan struct{}
	tok string
	exp time.Time
}

func newAppSource(clk clock.Clock, exchange exchangeFunc) *appSource {
	return &appSource{clock: clk, exchange: exchange, sem: make(chan struct{}, 1)}
}

func (a *appSource) lock(ctx context.Context) error {
	select {
	case a.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *appSource) unlock() { <-a.sem }

func (a *appSource) Token(ctx context.Context) (string, error) {
	if err := a.lock(ctx); err != nil {
		return "", err
	}
	defer a.unlock()
	now := a.clock.Now()
	if a.tok != "" && now.Before(a.exp.Add(-refreshMargin)) {
		return a.tok, nil
	}
	tok, exp, err := a.exchange(ctx)
	if err != nil {
		if a.tok != "" && a.clock.Now().Before(a.exp) {
			return a.tok, nil
		}
		return "", fmt.Errorf("github: installation token: %w", err)
	}
	a.tok, a.exp = tok, exp
	return tok, nil
}

func (a *appSource) Invalidate(ctx context.Context, token string) {
	if a.lock(ctx) != nil {
		return
	}
	defer a.unlock()
	if a.tok == token {
		a.tok, a.exp = "", time.Time{}
	}
}
