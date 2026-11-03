package webhook

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/go-github/v93/github"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/demand"
)

// MaxBodyBytes is the largest delivery body accepted.
const MaxBodyBytes = 1 << 20

const (
	actionQueued     = "queued"
	actionInProgress = "in_progress"
	actionCompleted  = "completed"
)

// Demand receives the jobs the handler accepts. The calls must not block,
// because the handler makes them before it responds (requirement
// INTAKE-4).
type Demand interface {
	Queued(j demand.Job)
	Started(id int64)
	Completed(id int64)
}

// Config configures a Handler.
type Config struct {
	// Secret is the webhook secret. Required.
	Secret config.Secret
	// Demand is required.
	Demand Demand
	// Pools in configuration order. Jobs that match none are ignored.
	Pools []config.Pool
	// Clock is nil for clock.Real().
	Clock clock.Clock
	// Logger is nil for slog.Default().
	Logger *slog.Logger
	// Metrics is nil for NopMetrics{}.
	Metrics Metrics
}

// Handler serves GitHub webhook deliveries.
type Handler struct {
	secret     []byte
	demand     Demand
	pools      []config.Pool
	logger     *slog.Logger
	metrics    Metrics
	deliveries *deliverySet
}

// New returns a Handler. It fails when Secret or Demand is unset.
func New(cfg Config) (*Handler, error) {
	if !cfg.Secret.IsSet() {
		return nil, errors.New("webhook: secret is required")
	}
	if cfg.Demand == nil {
		return nil, errors.New("webhook: demand is required")
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Metrics == nil {
		cfg.Metrics = NopMetrics{}
	}
	return &Handler{
		secret:     cfg.Secret.Value(),
		demand:     cfg.Demand,
		pools:      cfg.Pools,
		logger:     cfg.Logger,
		metrics:    cfg.Metrics,
		deliveries: newDeliverySet(cfg.Clock),
	}, nil
}

// ServeHTTP checks one delivery and hands an accepted job to Demand. It
// accepts any method, so the route must restrict it to POST.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	delivery := r.Header.Get("X-GitHub-Delivery")
	event := r.Header.Get("X-GitHub-Event")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			h.reply(w, http.StatusRequestEntityTooLarge, "", OutcomeTooLarge)
			return
		}
		h.reply(w, http.StatusBadRequest, "", OutcomeBadRequest)
		return
	}
	if !verifySignature(h.secret, body, r.Header.Get("X-Hub-Signature-256")) {
		h.logger.Warn("webhook: bad signature", "delivery", delivery, "remote_addr", r.RemoteAddr)
		h.reply(w, http.StatusUnauthorized, "", OutcomeBadSignature)
		return
	}
	if event == "" || delivery == "" {
		h.reply(w, http.StatusBadRequest, "", OutcomeBadRequest)
		return
	}
	if event != "workflow_job" {
		h.logger.Debug("webhook: event ignored", "delivery", delivery, "event", event)
		h.reply(w, http.StatusOK, "", OutcomeIgnored)
		return
	}
	var ev github.WorkflowJobEvent
	if err := json.Unmarshal(body, &ev); err != nil || ev.GetWorkflowJob().GetID() == 0 || ev.GetRepo().GetFullName() == "" {
		h.reply(w, http.StatusBadRequest, "", OutcomeBadRequest)
		return
	}
	action, wj, repo := ev.GetAction(), ev.GetWorkflowJob(), ev.GetRepo().GetFullName()
	log := h.logger.With("delivery", delivery, "action", action, "job_id", wj.GetID(),
		"repo", repo, "runner_name", wj.GetRunnerName())
	var handOff func()
	switch action {
	case actionQueued:
		handOff = func() {
			h.demand.Queued(demand.Job{ID: wj.GetID(), Labels: wj.Labels, Repo: repo, QueuedAt: wj.GetCreatedAt().Time})
		}
	case actionInProgress:
		handOff = func() { h.demand.Started(wj.GetID()) }
	case actionCompleted:
		handOff = func() { h.demand.Completed(wj.GetID()) }
	default:
		log.Debug("webhook: action ignored")
		h.reply(w, http.StatusOK, action, OutcomeIgnored)
		return
	}
	if _, ok := demand.Match(h.pools, wj.Labels, repo); !ok {
		log.Debug("webhook: job matches no pool", "labels", wj.Labels)
		h.reply(w, http.StatusOK, action, OutcomeIgnored)
		return
	}
	if !h.deliveries.firstSeen(delivery) {
		log.Debug("webhook: duplicate delivery")
		h.reply(w, http.StatusOK, action, OutcomeDuplicate)
		return
	}
	handOff()
	log.Debug("webhook: delivery handled")
	h.reply(w, http.StatusOK, action, OutcomeHandled)
}

func (h *Handler) reply(w http.ResponseWriter, status int, action, outcome string) {
	h.metrics.ObserveDelivery(action, outcome)
	w.WriteHeader(status)
}
