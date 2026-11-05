// Package daemon runs musterd: the startup sequence of design §9, the
// intakes, the dispatcher and the listeners, and the drain on a signal
// (docs/adr/2026-11-05-musterd-lifecycle.md).
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/demand"
	"github.com/liquidmetal-dev/muster/internal/dispatch"
	"github.com/liquidmetal-dev/muster/internal/github"
	"github.com/liquidmetal-dev/muster/internal/health"
	"github.com/liquidmetal-dev/muster/internal/intake/poller"
	"github.com/liquidmetal-dev/muster/internal/intake/webhook"
	"github.com/liquidmetal-dev/muster/internal/metrics"
	"github.com/liquidmetal-dev/muster/internal/pools"
	"github.com/liquidmetal-dev/muster/internal/reconcile"
	"github.com/liquidmetal-dev/muster/internal/server"
	"github.com/liquidmetal-dev/muster/internal/session"
	"github.com/liquidmetal-dev/muster/internal/version"
	"github.com/liquidmetal-dev/muster/internal/vmsource/battery"
	"github.com/liquidmetal-dev/muster/internal/vmsource/flintlock"
)

// shutdownTimeout bounds how long a listener waits for open requests at
// shutdown.
const shutdownTimeout = 5 * time.Second

// Options configures Run.
type Options struct {
	// Config is the configuration from config.Load. It is required.
	Config     *config.Config
	PrunePools bool
	// Logger is nil for slog.Default().
	Logger *slog.Logger
	// Signals delivers SIGTERM and SIGINT. The first starts shutdown; the
	// second ends the shutdown grace.
	Signals <-chan os.Signal
	// OpsListener and WebhookListener are nil to listen on
	// Config.MetricsAddr and Config.Listen.Addr.
	OpsListener     net.Listener
	WebhookListener net.Listener
}

type daemon struct {
	cfg  *config.Config
	opts Options
	log  *slog.Logger

	gh        *github.Client
	execer    *flintlock.Execer
	src       *battery.Source
	admin     *battery.Admin
	demand    *demand.Registry
	sessions  *session.Registry
	disp      *dispatch.Dispatcher
	readiness *health.Readiness
	poller    *poller.Poller
	metrics   *metrics.Metrics
	ops       *server.Server
	webhook   *server.Server
}

// Run starts musterd and serves until the first signal, then drains. It
// returns nil after a clean stop, including a signal during startup, or an
// error that names the failed startup step or listener.
func Run(ctx context.Context, opts Options) error {
	if opts.Config == nil {
		return errors.New("daemon: Config is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	stop, force, cancelSignals := stopContexts(ctx, opts.Signals)
	defer cancelSignals()

	d := &daemon{cfg: opts.Config, opts: opts, log: opts.Logger}
	if err := d.build(); err != nil {
		return err
	}
	defer d.close()
	return d.run(stop, force)
}

func (d *daemon) build() error {
	cfg, l := d.cfg, d.log
	names := make([]string, len(cfg.Pools))
	for i, p := range cfg.Pools {
		names[i] = p.Name
	}
	d.metrics = metrics.New(metrics.Config{
		Pools:    names,
		Sessions: metrics.SessionsFunc(func() []session.Info { return d.sessions.Sessions() }),
		Version:  version.Get(),
	})

	ghCfg, err := github.FromConfig(cfg)
	if err != nil {
		return fmt.Errorf("startup: github: %w", err)
	}
	ghCfg.Logger, ghCfg.Metrics = l, d.metrics
	if d.gh, err = github.New(ghCfg); err != nil {
		return fmt.Errorf("startup: github: %w", err)
	}

	if d.execer, err = flintlock.NewExecer(cfg.Flintlock, flintlock.WithLogger(l)); err != nil {
		return fmt.Errorf("startup: flintlock: %w", err)
	}
	d.src, err = battery.New(battery.Config{
		Addr:      cfg.Battery.Addr,
		Namespace: cfg.Battery.Namespace,
		TLS:       cfg.Battery.TLS,
		Logger:    l,
		Execer:    d.execer,
	})
	if err != nil {
		return fmt.Errorf("startup: battery: %w", err)
	}
	d.admin = d.src.Admin()

	d.demand = demand.New(demand.Config{Pools: cfg.Pools, Logger: l})
	d.sessions, err = session.New(session.Config{
		Pools:     cfg.Pools,
		Namespace: cfg.Battery.Namespace,
		Defaults:  cfg.Defaults,
		Source:    d.src,
		GitHub:    d.gh,
		Demand:    d.demand,
		Logger:    l,
		Metrics:   d.metrics,
	})
	if err != nil {
		return fmt.Errorf("startup: sessions: %w", err)
	}
	d.disp = dispatch.New(dispatch.Config{Pools: cfg.Pools, Demand: d.demand, Sessions: d.sessions, Logger: l})
	d.demand.SetOnChange(d.disp.Wake)

	d.readiness = health.New(health.Config{
		Probes: []health.Probe{
			{Name: "battery", Check: func(ctx context.Context) error {
				_, err := d.admin.ListHosts(ctx)
				return err
			}},
			{Name: "github", Check: d.gh.Ping},
		},
		Logger:  l,
		Metrics: d.metrics,
	})

	if d.poller, err = poller.New(poller.Config{Poll: cfg.GitHub.Poll, Lister: d.gh, Demand: d.demand, Logger: l, Metrics: d.metrics}); err != nil {
		return fmt.Errorf("startup: poller: %w", err)
	}
	if cfg.Listen != nil {
		h, err := webhook.New(webhook.Config{
			Secret:  cfg.GitHub.WebhookSecret,
			Demand:  d.demand,
			Pools:   cfg.Pools,
			Logger:  l,
			Metrics: d.metrics,
			Runners: d.sessions,
		})
		if err != nil {
			return fmt.Errorf("startup: webhook: %w", err)
		}
		if d.webhook, err = server.New(server.Config{Listen: *cfg.Listen, Webhook: h, Logger: l}); err != nil {
			return fmt.Errorf("startup: webhook listener: %w", err)
		}
	}
	d.ops, err = server.NewOps(server.OpsConfig{
		Addr:    cfg.MetricsAddr,
		Metrics: d.metrics.Handler(),
		Ready:   d.readiness.Handler(),
		Logger:  l,
	})
	if err != nil {
		return fmt.Errorf("startup: ops listener: %w", err)
	}
	if cfg.API != nil {
		l.Warn("gRPC API is not implemented yet; api is ignored", "addr", cfg.API.Addr)
	}
	return nil
}

func (d *daemon) close() {
	_ = d.src.Close()
	_ = d.execer.Close()
}

// group runs goroutines and waits for them.
type group struct{ wg sync.WaitGroup }

func (g *group) goCtx(ctx context.Context, fn func(context.Context)) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	g.wg.Go(func() { defer close(done); fn(ctx) })
	return func() { cancel(); <-done }
}

func listenOn(given net.Listener, addr, name string) (net.Listener, error) {
	if given != nil {
		return given, nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("startup: %s %s: %w", name, addr, err)
	}
	return ln, nil
}

func (d *daemon) run(stop, force context.Context) error {
	var g group
	defer g.wg.Wait()
	serveErr := make(chan error, 2)
	serve := func(s *server.Server, ln net.Listener, name string) {
		g.wg.Go(func() {
			if err := s.Serve(ln); err != nil {
				serveErr <- fmt.Errorf("%s: %w", name, err)
			}
		})
	}
	// The listeners and goroutines outlive stop, so they hang off a
	// context that only Run's own exit cancels.
	base := context.WithoutCancel(stop)

	opsLn, err := listenOn(d.opts.OpsListener, d.cfg.MetricsAddr, "ops listener")
	if err != nil {
		return err
	}
	serve(d.ops, opsLn, "ops listener")
	defer d.shutdownServer(d.ops)
	stopProbes := g.goCtx(base, d.readiness.Run)
	defer stopProbes()

	hc := pools.NewHostCheck(d.admin)
	err = config.Check(stop, d.cfg, github.Checker{Client: d.gh}, hc, flintlock.NewChecker(d.execer, hc.Resolve, d.log))
	if err != nil {
		return d.startupErr(stop, "config check", err)
	}
	res, err := pools.Reconcile(stop, pools.Config{
		Namespace: d.cfg.Battery.Namespace,
		Pools:     d.cfg.Pools,
		Defaults:  d.cfg.Defaults,
		Admin:     d.admin,
		Prune:     d.opts.PrunePools,
		Logger:    d.log,
	})
	if err != nil {
		return d.startupErr(stop, "pool reconciliation", err)
	}
	d.log.Info("pools reconciled", "created", len(res.Created), "updated", len(res.Updated), "orphans", len(res.Orphans), "pruned", len(res.Pruned))

	sessCtx, cancelSessions := context.WithCancel(base)
	sessDone := make(chan struct{})
	g.wg.Go(func() { defer close(sessDone); _ = d.sessions.Run(sessCtx) })
	stopSessions := func() { cancelSessions(); <-sessDone }
	select {
	case <-d.sessions.Running():
	case <-stop.Done():
		stopSessions()
		return nil
	}
	if _, err := reconcile.Run(stop, reconcile.Config{
		Namespace: d.cfg.Battery.Namespace,
		Pools:     d.cfg.Pools,
		Source:    d.src,
		GitHub:    d.gh,
		Sessions:  d.sessions,
		Metrics:   d.metrics,
		Logger:    d.log,
	}); err != nil {
		stopSessions()
		return d.startupErr(stop, "session reconciliation", err)
	}

	stopDispatch := g.goCtx(base, d.disp.Run)
	defer stopDispatch()
	stopPoller := g.goCtx(base, d.poller.Run)
	defer stopPoller()
	if d.webhook != nil {
		ln, err := listenOn(d.opts.WebhookListener, d.cfg.Listen.Addr, "webhook listener")
		if err != nil {
			stopSessions()
			return err
		}
		serve(d.webhook, ln, "webhook listener")
	}
	if stop.Err() != nil {
		d.shutdown(force, stopPoller, stopSessions)
		return nil
	}
	d.readiness.MarkStarted()
	d.log.Info("musterd ready")

	var runErr error
	select {
	case <-stop.Done():
		d.log.Info("musterd stopping")
	case runErr = <-serveErr:
		d.log.Error("musterd stopping after a listener failed", "err", runErr)
	}
	d.shutdown(force, stopPoller, stopSessions)
	return runErr
}

func (d *daemon) startupErr(stop context.Context, step string, err error) error {
	if stop.Err() != nil {
		return nil
	}
	return fmt.Errorf("startup: %s: %w", step, err)
}

func (d *daemon) shutdown(force context.Context, stopPoller, stopSessions func()) {
	d.readiness.MarkStopping()
	// Drain before the intakes stop: a webhook request still open hands
	// off its job while the listener shuts down.
	d.disp.Drain()
	if d.webhook != nil {
		d.shutdownServer(d.webhook)
	}
	stopPoller()
	reconcile.Shutdown(force, reconcile.ShutdownConfig{
		Dispatcher: d.disp,
		Sessions:   d.sessions,
		Stop:       stopSessions,
		Grace:      d.cfg.ShutdownGrace.Duration(),
		Logger:     d.log,
	})
}

func (d *daemon) shutdownServer(s *server.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		d.log.Warn("listener shutdown", "err", err)
	}
}
