package pools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	poolmgrv1alpha1 "github.com/liquidmetal-dev/battery/api/proto/poolmgr/v1alpha1"
	"google.golang.org/protobuf/proto"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/vmsource"
)

// Admin is the battery pool administration that Reconcile uses. Every
// call is scoped to muster's namespace.
type Admin interface {
	ListPools(ctx context.Context) ([]*poolmgrv1alpha1.PoolSpec, error)
	GetPool(ctx context.Context, name string) (*poolmgrv1alpha1.PoolSpec, error)
	CreatePool(ctx context.Context, spec *poolmgrv1alpha1.PoolSpec) error
	UpdatePool(ctx context.Context, spec *poolmgrv1alpha1.PoolSpec) error
	DeletePool(ctx context.Context, name string) error
}

// Config is the input of Reconcile.
type Config struct {
	Admin  Admin
	Config *config.Config
	// Prune deletes orphan pools (POOL-7). Without it they are only logged.
	Prune  bool
	Logger *slog.Logger
}

// Result names the pools by what Reconcile did to them.
type Result struct {
	Created, Updated, Unchanged []string
	// Orphans are the pools in the namespace that the configuration does
	// not name; Pruned is the subset that was deleted.
	Orphans, Pruned []string
}

// Reconcile makes the pools in the namespace equal the configured pools.
// It applies every configured pool, then handles orphans, and returns
// every error joined, so one bad pool does not hide another.
func Reconcile(ctx context.Context, cfg Config) (Result, error) {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	ns := cfg.Config.Battery.Namespace
	var res Result
	var errs []error
	configured := map[string]bool{}
	for _, p := range cfg.Config.Pools {
		if err := ctx.Err(); err != nil {
			return res, errors.Join(append(errs, err)...)
		}
		configured[p.Name] = true
		outcome, err := apply(ctx, cfg.Admin, Spec(ns, p, cfg.Config.Defaults))
		if err != nil {
			errs = append(errs, fmt.Errorf("pool %s: %w", p.Name, err))
			continue
		}
		switch outcome {
		case created:
			res.Created = append(res.Created, p.Name)
		case updated:
			res.Updated = append(res.Updated, p.Name)
		default:
			res.Unchanged = append(res.Unchanged, p.Name)
		}
	}

	listed, err := cfg.Admin.ListPools(ctx)
	if err != nil {
		return res, errors.Join(append(errs, fmt.Errorf("list pools: %w", err))...)
	}
	for _, spec := range listed {
		name := spec.GetName()
		if configured[name] {
			continue
		}
		res.Orphans = append(res.Orphans, name)
		if !cfg.Prune {
			log.Warn("orphan pool; pass --prune-pools to delete it", "namespace", ns, "pool", name)
			continue
		}
		err := cfg.Admin.DeletePool(ctx, name)
		switch {
		case err == nil:
			log.Info("orphan pool pruned", "namespace", ns, "pool", name)
			res.Pruned = append(res.Pruned, name)
		case errors.Is(err, vmsource.ErrPoolInUse):
			log.Warn("orphan pool still has leased VMs; a later start prunes it", "namespace", ns, "pool", name)
		default:
			errs = append(errs, fmt.Errorf("prune pool %s: %w", name, err))
		}
	}
	return res, errors.Join(errs...)
}

type outcome int

const (
	unchanged outcome = iota
	created
	updated
)

func apply(ctx context.Context, admin Admin, want *poolmgrv1alpha1.PoolSpec) (outcome, error) {
	got, err := admin.GetPool(ctx, want.GetName())
	if errors.Is(err, vmsource.ErrPoolNotFound) {
		err = admin.CreatePool(ctx, want)
		if err == nil {
			return created, nil
		}
		if !errors.Is(err, vmsource.ErrPoolExists) {
			return unchanged, err
		}
		got, err = admin.GetPool(ctx, want.GetName())
	}
	if err != nil {
		return unchanged, err
	}
	if proto.Equal(got, want) {
		return unchanged, nil
	}
	if err := admin.UpdatePool(ctx, want); err != nil {
		return unchanged, err
	}
	return updated, nil
}
