package config

import (
	"context"
	"errors"
	"fmt"
)

// Checker is one live validation (battery host existence and exec
// enablement, GitHub credential reach) supplied by the package that owns
// the client, so musterd and musterctl config validate share one code
// path (requirement REL-5). Name prefixes its errors.
type Checker interface {
	Name() string
	Check(ctx context.Context, cfg *Config) error
}

// Check runs every checker in order and returns all failures joined, each
// as "<name>: <error>". Before each checker ctx.Err() is consulted: a
// cancelled ctx stops the run and the ctx error is included. Timeouts
// belong to the caller's ctx.
func Check(ctx context.Context, cfg *Config, checkers ...Checker) error {
	var errs []error
	for _, c := range checkers {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := c.Check(ctx, cfg); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.Name(), err))
		}
	}
	return errors.Join(errs...)
}
