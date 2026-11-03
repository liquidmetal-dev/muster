package github

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	gogithub "github.com/google/go-github/v93/github"
)

const (
	endpointOrgJITConfig   = "POST /orgs/{org}/actions/runners/generate-jitconfig"
	endpointRepoJITConfig  = "POST /repos/{owner}/{repo}/actions/runners/generate-jitconfig"
	endpointOrgRunners     = "GET /orgs/{org}/actions/runners"
	endpointRepoRunners    = "GET /repos/{owner}/{repo}/actions/runners"
	endpointOrgRunnerDel   = "DELETE /orgs/{org}/actions/runners/{id}"
	endpointRepoRunnerDel  = "DELETE /repos/{owner}/{repo}/actions/runners/{id}"
	endpointOrgRunnerGroup = "GET /orgs/{org}/actions/runner-groups"

	// maxRunnerName is GitHub's limit on a runner name.
	maxRunnerName     = 64
	defaultWorkFolder = "_work"
	// defaultRunnerGroupID is the group every repository runner joins.
	defaultRunnerGroupID = 1
	perPage              = 100
)

// JITRequest describes a just-in-time runner (requirement SESSION-4).
type JITRequest struct {
	// Name is at most 64 characters and unique within the scope.
	Name   string
	Labels []string
	// RunnerGroupID comes from RunnerGroupID.
	RunnerGroupID int64
	// WorkFolder defaults to "_work".
	WorkFolder string
}

// JITConfig is a registered runner and the configuration that starts it.
type JITConfig struct {
	RunnerID int64
	Encoded  EncodedJITConfig
}

// EncodedJITConfig is a secret: anyone who holds it can run a runner in the
// scope (requirement SESSION-9). Every formatting path prints a placeholder;
// Reveal is the only way to read it.
type EncodedJITConfig string

const redacted = "[redacted]"

func (EncodedJITConfig) String() string { return redacted }

// GoString keeps %#v from printing the value.
func (EncodedJITConfig) GoString() string { return redacted }

// Format keeps every fmt verb from printing the value, including verbs that
// do not apply to strings, whose error output would otherwise show it.
func (EncodedJITConfig) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// LogValue keeps slog from printing the value.
func (EncodedJITConfig) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON keeps json.Marshal and slog's JSON handler, which marshals
// structs without consulting LogValue on their fields, from printing the
// value.
func (EncodedJITConfig) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// MarshalText keeps text encoders from printing the value.
func (EncodedJITConfig) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Reveal returns the encoded configuration for the runner.
func (e EncodedJITConfig) Reveal() string { return string(e) }

// Runner is a self-hosted runner registered with GitHub.
type Runner struct {
	ID     int64
	Name   string
	Status string
	Busy   bool
	Labels []string
}

func (s Scope) valid() error {
	org := s.Org != "" && s.Owner == "" && s.Repo == ""
	repo := s.Org == "" && s.Owner != "" && s.Repo != ""
	if !org && !repo {
		return fmt.Errorf("%w: scope %+v", ErrInvalidRequest, s)
	}
	return nil
}

// withoutResponseMessage keeps the status of a GitHub error response and
// drops its message and error details, which can echo the request.
func withoutResponseMessage(err error) error {
	if resp := errorResponse(err); resp != nil {
		return &gogithub.ErrorResponse{Response: resp.Response}
	}
	return err
}

// GenerateJITConfig registers a runner and returns its JIT configuration.
// The request is never retried after a server error, so one call registers
// at most one runner.
func (c *Client) GenerateJITConfig(ctx context.Context, s Scope, r JITRequest) (JITConfig, error) {
	if err := s.valid(); err != nil {
		return JITConfig{}, err
	}
	if r.Name == "" || len(r.Name) > maxRunnerName {
		return JITConfig{}, fmt.Errorf("%w: runner name must be 1 to %d characters", ErrInvalidRequest, maxRunnerName)
	}
	if len(r.Labels) == 0 {
		return JITConfig{}, fmt.Errorf("%w: runner needs at least one label", ErrInvalidRequest)
	}
	folder := r.WorkFolder
	if folder == "" {
		folder = defaultWorkFolder
	}
	body := gogithub.CreateJITConfigRequest{Name: r.Name, Labels: r.Labels, RunnerGroupID: r.RunnerGroupID, WorkFolder: &folder}
	var (
		out *gogithub.JITRunnerConfig
		err error
	)
	if s.IsOrg() {
		out, _, err = c.gh.Actions.CreateOrgJITConfig(withEndpoint(ctx, endpointOrgJITConfig), s.Org, body)
	} else {
		out, _, err = c.gh.Actions.CreateRepoJITConfig(withEndpoint(ctx, endpointRepoJITConfig), s.Owner, s.Repo, body)
	}
	err = withoutResponseMessage(err)
	if statusOf(err) == http.StatusConflict {
		return JITConfig{}, fmt.Errorf("%w: %q in %s: %w", ErrRunnerExists, r.Name, s, err)
	}
	if err != nil {
		return JITConfig{}, mapError(err)
	}
	return JITConfig{RunnerID: out.GetRunner().GetID(), Encoded: EncodedJITConfig(out.GetEncodedJITConfig())}, nil
}

// ListRunners returns every runner in the scope whose name starts with
// prefix. GitHub only filters by exact name, so the client pages through all
// runners and filters them itself.
func (c *Client) ListRunners(ctx context.Context, s Scope, prefix string) ([]Runner, error) {
	if err := s.valid(); err != nil {
		return nil, err
	}
	opts := &gogithub.ListRunnersOptions{ListOptions: gogithub.ListOptions{PerPage: perPage}}
	var out []Runner
	for {
		var (
			page *gogithub.Runners
			resp *gogithub.Response
			err  error
		)
		if s.IsOrg() {
			page, resp, err = c.gh.Actions.ListOrganizationRunners(withEndpoint(ctx, endpointOrgRunners), s.Org, opts)
		} else {
			page, resp, err = c.gh.Actions.ListRunners(withEndpoint(ctx, endpointRepoRunners), s.Owner, s.Repo, opts)
		}
		if err != nil {
			return nil, mapError(err)
		}
		for _, r := range page.Runners {
			if strings.HasPrefix(r.GetName(), prefix) {
				out = append(out, toRunner(r))
			}
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

func toRunner(r *gogithub.Runner) Runner {
	labels := make([]string, 0, len(r.Labels))
	for _, l := range r.Labels {
		labels = append(labels, l.GetName())
	}
	return Runner{ID: r.GetID(), Name: r.GetName(), Status: r.GetStatus(), Busy: r.GetBusy(), Labels: labels}
}

// DeleteRunner removes a runner. A runner that is already gone is not an
// error; a runner that is running a job returns ErrRunnerBusy.
func (c *Client) DeleteRunner(ctx context.Context, s Scope, id int64) error {
	if err := s.valid(); err != nil {
		return err
	}
	var err error
	if s.IsOrg() {
		_, err = c.gh.Actions.RemoveOrganizationRunner(withEndpoint(ctx, endpointOrgRunnerDel), s.Org, id)
	} else {
		_, err = c.gh.Actions.RemoveRunner(withEndpoint(ctx, endpointRepoRunnerDel), s.Owner, s.Repo, id)
	}
	switch statusOf(err) {
	case http.StatusNotFound:
		return nil
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: runner %d in %s: %w", ErrRunnerBusy, id, s, err)
	}
	return mapError(err)
}

// RunnerGroupID resolves a runner group name for JITRequest. Repository
// runners always join the default group, so repository scope needs no call.
// In organization scope an empty name selects the default group, and names
// compare case-insensitively.
func (c *Client) RunnerGroupID(ctx context.Context, s Scope, name string) (int64, error) {
	if err := s.valid(); err != nil {
		return 0, err
	}
	if !s.IsOrg() {
		return defaultRunnerGroupID, nil
	}
	opts := &gogithub.ListOrgRunnerGroupOptions{ListOptions: gogithub.ListOptions{PerPage: perPage}}
	for {
		page, resp, err := c.gh.Actions.ListOrganizationRunnerGroups(withEndpoint(ctx, endpointOrgRunnerGroup), s.Org, opts)
		if err != nil {
			return 0, mapError(err)
		}
		for _, g := range page.RunnerGroups {
			if (name == "" && g.GetDefault()) || (name != "" && strings.EqualFold(g.GetName(), name)) {
				return g.GetID(), nil
			}
		}
		if resp.NextPage == 0 {
			return 0, fmt.Errorf("%w: %q in %s", ErrRunnerGroupNotFound, name, s)
		}
		opts.Page = resp.NextPage
	}
}
