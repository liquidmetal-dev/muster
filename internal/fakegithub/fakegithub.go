// Package fakegithub is an in-memory GitHub REST server for tests. It serves
// the endpoints muster uses with the status codes of the real API: 401 for
// bad credentials or a bad App JWT, 403 for an installation token on
// /user, 404 for an unknown installation, organization, repository, runner
// group, runner or run, 409 for a duplicate runner name, 422 for an invalid
// JIT request or a busy runner. Response bodies are serialised from go-github
// types, so the real client decodes them unchanged.
//
// Authentication is strict. POST /app/installations/{id}/access_tokens and
// GET /app/installations/{id} need an RS256 App JWT signed by the configured
// key with iss equal to AppID. Every other route needs an unexpired
// installation token or a configured personal access token, in either the
// "Bearer" or the "token" scheme.
//
// All time checks run on Config.Clock: JWT exp and iat, token expiry and
// the rate-limit reset header. A test that drives the fake with a manual
// clock must mint its JWTs from that clock. A client that mints JWTs from
// wall time must run the fake on clock.Real() and prove token refresh with
// a short TokenTTL.
//
// Tests seed the model with AddOrg, AddRepo and AddRunnerGroup; every org
// gets a Default runner group with id 1. QueueJob stores a queued job as its
// own run and POSTs a signed workflow_job.queued event to the webhook URL.
// Deliveries are synchronous, never retried, and always recorded, so
// Deliveries doubles as the event log; Redeliver repeats one. Tests inject faults with FailNext and Stall, keyed by the route pattern
// constants, and observe traffic with Requests and RequestCount.
package fakegithub

import (
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
)

// Config configures a Server. Zero values take the documented defaults.
type Config struct {
	// AppID is the GitHub App id that JWTs must name in iss.
	AppID int64
	// AppPublicKey verifies the RS256 signature of App JWTs.
	AppPublicKey *rsa.PublicKey
	// InstallationID is the only installation the fake knows.
	InstallationID int64
	// InstallationAccount is the login GET /app/installations/{id} returns.
	InstallationAccount string
	// TokenTTL is the lifetime of an installation token. Default one hour.
	TokenTTL time.Duration
	// PATs maps accepted personal access tokens to the user login they belong to.
	PATs map[string]string
	// WebhookURL receives signed workflow_job deliveries. Empty disables delivery.
	WebhookURL string
	// WebhookSecret signs deliveries with HMAC-SHA256.
	WebhookSecret string
	// Clock drives every time check and timer. Default clock.Real().
	Clock clock.Clock
}

// Request is one logged HTTP request.
type Request struct {
	Method, Path, Pattern string
	Status                int
	// Auth is how the request authenticated: "jwt", "installation", "pat",
	// "none" or "invalid".
	Auth string
}

// Fault is an injected error response.
type Fault struct {
	Status int
	Header http.Header
	// Message is the body's "message" field.
	Message string
}

// Server is the fake. Use New.
type Server struct {
	cfg   Config
	clock clock.Clock
	mux   *http.ServeMux
	auth  map[string]authReq

	mu            sync.Mutex
	nextID        int64 // shared by repos, runners, runs and jobs
	orgs          map[string]*orgState
	repos         map[string]*repoState
	runners       map[int64]*runnerState
	jobs          map[int64]*jobState
	runs          map[int64]int64 // run id -> job id
	deliveries    []Delivery
	defaults      Behavior
	nextBehaviors []Behavior
	tokens        map[string]time.Time // installation token -> expiry
	requests      []Request
	failNext      map[string][]Fault
	stallNext     map[string][]time.Duration

	closeOnce sync.Once
	done      chan struct{}
	wakeCh    chan struct{}
	wg        sync.WaitGroup
}

// New returns a fake with the given configuration.
func New(cfg Config) *Server {
	if cfg.TokenTTL == 0 {
		cfg.TokenTTL = time.Hour
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	s := &Server{
		cfg:       cfg,
		clock:     cfg.Clock,
		mux:       http.NewServeMux(),
		auth:      map[string]authReq{},
		orgs:      map[string]*orgState{},
		repos:     map[string]*repoState{},
		runners:   map[int64]*runnerState{},
		jobs:      map[int64]*jobState{},
		runs:      map[int64]int64{},
		tokens:    map[string]time.Time{},
		failNext:  map[string][]Fault{},
		stallNext: map[string][]time.Duration{},
		done:      make(chan struct{}),
		wakeCh:    make(chan struct{}, 1),
		defaults:  packageDefaults,
	}
	s.registerRoutes()
	s.wg.Add(1)
	go s.run()
	return s
}

// Handler returns the HTTP handler for the fake.
func (s *Server) Handler() http.Handler { return s }

// Start serves the fake on a loopback httptest server for the life of the
// test and returns its base URL without a trailing slash.
func (s *Server) Start(tb testing.TB) string {
	tb.Helper()
	srv := httptest.NewServer(s.Handler())
	tb.Cleanup(func() {
		srv.Close()
		s.Close()
	})
	return srv.URL
}

// Close stops the lifecycle scheduler and waits for it. It is idempotent.
func (s *Server) Close() {
	s.closeOnce.Do(func() { close(s.done) })
	s.wg.Wait()
}

// Requests returns a copy of the request log in arrival order. A request
// still in flight has Status 0.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// RequestCount returns how many logged requests matched pattern.
func (s *Server) RequestCount(pattern string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r.Pattern == pattern {
			n++
		}
	}
	return n
}

// FailNext makes the next request that matches pattern fail with f, before
// authentication. Repeated calls stack in order.
func (s *Server) FailNext(pattern string, f Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[pattern] = append(s.failNext[pattern], f)
}

// Stall delays the next request that matches pattern by d of wall-clock
// time before it is handled. Repeated calls stack in order.
func (s *Server) Stall(pattern string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stallNext[pattern] = append(s.stallNext[pattern], d)
}

func (s *Server) popFault(pattern string) (Fault, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.failNext[pattern]
	if len(q) == 0 {
		return Fault{}, false
	}
	s.failNext[pattern] = q[1:]
	return q[0], true
}

func (s *Server) popStall(pattern string) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.stallNext[pattern]
	if len(q) == 0 {
		return 0, false
	}
	s.stallNext[pattern] = q[1:]
	return q[0], true
}

// logArrival appends r to the log at arrival and returns its index, so the
// log keeps arrival order even when a stalled request finishes late.
func (s *Server) logArrival(r Request) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r)
	return len(s.requests) - 1
}

// logOutcome fills in the status and auth kind of the request at slot.
func (s *Server) logOutcome(slot, status int, auth string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests[slot].Status = status
	s.requests[slot].Auth = auth
}
