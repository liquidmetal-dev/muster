package flintlock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// HostResolver maps battery host names to their flintlock endpoints. Names
// it does not return are skipped: rejecting unknown hosts is the job of
// pool reconciliation (POOL-3).
type HostResolver func(ctx context.Context, names []string) ([]vmsource.HostInfo, error)

// NewChecker returns the config.Checker that runs CheckHost on every
// flintlock host of every pool. Any error it returns fails validation, so
// it suits `musterctl config validate`. Pool reconciliation calls
// Execer.CheckHost itself, so that it refuses only the pools of a bad host
// (POOL-4).
//
// A host that did not answer (Unavailable or DeadlineExceeded) is logged
// and passes, because the first Exec on it checks it again.
func NewChecker(e *Execer, resolve HostResolver, logger *slog.Logger) config.Checker {
	if logger == nil {
		logger = slog.Default()
	}
	return checker{e: e, resolve: resolve, logger: logger}
}

type checker struct {
	e       *Execer
	resolve HostResolver
	logger  *slog.Logger
}

func (checker) Name() string { return "flintlock" }

func (c checker) Check(ctx context.Context, cfg *config.Config) error {
	poolsOf := map[string][]string{}
	for _, p := range cfg.Pools {
		for _, h := range p.FlintlockHosts {
			poolsOf[h] = append(poolsOf[h], p.Name)
		}
	}
	names := make([]string, 0, len(poolsOf))
	for h := range poolsOf {
		names = append(names, h)
	}
	slices.Sort(names)
	if err := ctx.Err(); err != nil {
		return err
	}
	hosts, err := c.resolve(ctx, names)
	if err != nil {
		return err
	}
	slices.SortFunc(hosts, func(a, b vmsource.HostInfo) int { return strings.Compare(a.Name, b.Name) })

	var errs []error
	for _, h := range hosts {
		pools, ok := poolsOf[h.Name]
		if !ok {
			continue
		}
		slices.Sort(pools)
		err := c.e.CheckHost(ctx, h)
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return errors.Join(append(errs, ctx.Err())...)
		case unreachable(err):
			c.logger.Warn("flintlock host did not answer; its first exec checks it again",
				"host", h.Name, "address", h.Address, "error", err)
		default:
			errs = append(errs, fmt.Errorf("pools %v: %w", pools, err))
		}
	}
	return errors.Join(errs...)
}

func unreachable(err error) bool {
	if errors.Is(err, vmsource.ErrExecUnavailable) {
		return false
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	}
	return false
}
