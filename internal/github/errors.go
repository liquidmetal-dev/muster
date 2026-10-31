package github

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrNotFound means GitHub answered 404 for the scope or object.
	ErrNotFound = errors.New("github: not found")
	// ErrUnauthorized means GitHub rejected the credential.
	ErrUnauthorized = errors.New("github: unauthorized")
	// ErrRateLimited means GitHub rate limited the call and the client gave
	// up waiting. A *RateLimitedError carries the reset time.
	ErrRateLimited = errors.New("github: rate limited")
	// ErrRunnerExists means a runner with the requested name is registered.
	ErrRunnerExists = errors.New("github: runner name already exists")
	// ErrRunnerBusy means the runner is running a job and cannot be deleted.
	ErrRunnerBusy = errors.New("github: runner is busy")
	// ErrRunnerGroupNotFound means no runner group has the requested name.
	ErrRunnerGroupNotFound = errors.New("github: runner group not found")
	// ErrInvalidRequest means the arguments were rejected before any call.
	ErrInvalidRequest = errors.New("github: invalid request")
)

// RateLimitedError is returned when GitHub asks the client to wait longer
// than Config.MaxWait, or the client ran out of attempts while rate limited.
type RateLimitedError struct {
	// Reset is when GitHub expects to accept calls again; zero if unknown.
	Reset    time.Time
	Endpoint string
}

func (e *RateLimitedError) Error() string {
	if e.Reset.IsZero() {
		return fmt.Sprintf("github: rate limited on %s", e.Endpoint)
	}
	return fmt.Sprintf("github: rate limited on %s until %s", e.Endpoint, e.Reset.UTC().Format(time.RFC3339))
}

// Is makes errors.Is(err, ErrRateLimited) match.
func (e *RateLimitedError) Is(target error) bool { return target == ErrRateLimited }
