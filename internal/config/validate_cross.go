package config

import (
	"strconv"
	"strings"
)

// validateCrossCutting holds the rules that span sections: at least one
// intake, polled repositories inside a pool scope, and on the GitHub App
// path one owner across every pool scope and polled repository (GH-3).
func validateCrossCutting(cfg *Config, errs *errorList) {
	if cfg.Listen == nil && !cfg.GitHub.Poll.Enabled {
		errs.add("", "at least one intake must be enabled: set listen or github.poll.enabled")
	}

	if cfg.GitHub.Poll.Enabled {
		for i, repo := range cfg.GitHub.Poll.Repos {
			if !isOwnerName(repo) {
				continue // validatePoll reported it
			}
			if !coveredByAnyPool(cfg.Pools, repo) {
				errs.add("github.poll.repos["+strconv.Itoa(i)+"]", "%q is not within any pool scope", repo)
			}
		}
	}

	if cfg.GitHub.App == nil {
		return
	}
	// The first pool with a valid scope sets the owner one App
	// installation can reach.
	first := -1
	for i := range cfg.Pools {
		if cfg.Pools[i].Scope.Owner() != "" {
			first = i
			break
		}
	}
	if first < 0 {
		return
	}
	want := cfg.Pools[first].Scope.Owner()
	mismatch := func(path, owner string) {
		errs.add(path, "owner %q does not match owner %q of %s; one GitHub App installation reaches one owner",
			owner, want, poolPath(first, &cfg.Pools[first]))
	}
	for i := first + 1; i < len(cfg.Pools); i++ {
		p := &cfg.Pools[i]
		if owner := p.Scope.Owner(); owner != "" && !strings.EqualFold(owner, want) {
			mismatch(poolPath(i, p)+".scope", owner)
		}
	}
	if cfg.GitHub.Poll.Enabled {
		for i, repo := range cfg.GitHub.Poll.Repos {
			owner, _, ok := strings.Cut(repo, "/")
			if ok && owner != "" && !strings.EqualFold(owner, want) {
				mismatch("github.poll.repos["+strconv.Itoa(i)+"]", owner)
			}
		}
	}
}

func coveredByAnyPool(pools []Pool, repo string) bool {
	for i := range pools {
		if pools[i].Scope.Covers(repo) {
			return true
		}
	}
	return false
}
