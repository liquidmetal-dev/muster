package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	gogithub "github.com/google/go-github/v93/github"

	"github.com/liquidmetal-dev/muster/internal/config"
)

// Checker is the live credential reach validation (requirements GH-3 and
// REL-5). It fails when the credential does not work, when an App
// installation belongs to an account other than a pool scope or polled
// repository owner, or when a probe of a scope or polled repository is
// refused.
type Checker struct {
	Client *Client
}

var _ config.Checker = Checker{}

// Name prefixes the checker's errors.
func (Checker) Name() string { return "github" }

// Check probes every pool scope and polled repository with the configured
// credential, one call each.
func (k Checker) Check(ctx context.Context, cfg *config.Config) error {
	c := k.Client
	if err := c.Ping(ctx); err != nil {
		return err
	}
	var login string
	if c.cfg.App != nil {
		inst, err := c.installation(ctx)
		if err != nil {
			return err
		}
		login = inst.GetAccount().GetLogin()
	}
	var errs []error
	ownerOK := func(path, owner string) bool {
		if login == "" || strings.EqualFold(owner, login) {
			return true
		}
		errs = append(errs, fmt.Errorf("%s: owner %q is not reachable by installation account %q", path, owner, login))
		return false
	}
	for i, p := range cfg.Pools {
		path := fmt.Sprintf("pools[%d].scope", i)
		if !ownerOK(path, p.Scope.Owner()) {
			continue
		}
		s, err := ScopeFromConfig(p.Scope)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		if err := c.probeScope(ctx, s); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s is not reachable: %w", path, s, err))
		}
	}
	for j, repo := range cfg.GitHub.Poll.Repos {
		path := fmt.Sprintf("github.poll.repos[%d]", j)
		owner, _, _ := strings.Cut(repo, "/")
		if !ownerOK(path, owner) {
			continue
		}
		if err := c.probeRepo(ctx, repo); err != nil {
			errs = append(errs, fmt.Errorf("%s: repo %q is not reachable: %w", path, repo, err))
		}
	}
	return errors.Join(errs...)
}

func (c *Client) probeScope(ctx context.Context, s Scope) error {
	opts := &gogithub.ListRunnersOptions{ListOptions: gogithub.ListOptions{PerPage: 1}}
	var err error
	if s.IsOrg() {
		_, _, err = c.gh.Actions.ListOrganizationRunners(withEndpoint(ctx, endpointOrgRunners), s.Org, opts)
	} else {
		_, _, err = c.gh.Actions.ListRunners(withEndpoint(ctx, endpointRepoRunners), s.Owner, s.Repo, opts)
	}
	return probeError(err)
}

func (c *Client) probeRepo(ctx context.Context, repo string) error {
	s, err := RepoScope(repo)
	if err != nil {
		return err
	}
	opts := &gogithub.ListWorkflowRunsOptions{Status: statusQueued, ListOptions: gogithub.ListOptions{PerPage: 1}}
	_, _, err = c.gh.Actions.ListRepositoryWorkflowRuns(withEndpoint(ctx, endpointRuns), s.Owner, s.Repo, opts)
	return probeError(err)
}

func probeError(err error) error {
	if statusOf(err) == http.StatusForbidden {
		return fmt.Errorf("forbidden: %w", err)
	}
	return mapError(err)
}
