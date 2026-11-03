package github

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
)

func TestAppTokenNotReusedWhenItExpiresDuringFailedRefresh(t *testing.T) {
	clk := clock.NewManual(time.Unix(1_000_000, 0))
	exp := clk.Now().Add(time.Hour)
	calls := 0
	src := newAppSource(clk, func(context.Context) (string, time.Time, error) {
		calls++
		if calls == 1 {
			return "old", exp, nil
		}
		clk.Set(exp.Add(time.Second))
		return "", time.Time{}, errors.New("exchange timed out")
	})
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	clk.Set(exp.Add(-time.Minute))
	if tok, err := src.Token(context.Background()); err == nil {
		t.Fatalf("Token = %q, nil; want the exchange error once the old token expired", tok)
	}
}
