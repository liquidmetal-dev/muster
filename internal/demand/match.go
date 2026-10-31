package demand

import (
	"slices"
	"strings"

	"github.com/liquidmetal-dev/muster/internal/config"
)

// Match returns the name of the first pool, in configuration order, whose
// labels include every job label and whose scope covers repo (POOL-5).
// Labels compare case-insensitively, as GitHub compares them.
func Match(pools []config.Pool, labels []string, repo string) (string, bool) {
	for _, p := range pools {
		if p.Scope.Covers(repo) && hasAllLabels(p.Labels, labels) {
			return p.Name, true
		}
	}
	return "", false
}

func hasAllLabels(have, want []string) bool {
	for _, w := range want {
		if !slices.ContainsFunc(have, func(h string) bool { return strings.EqualFold(h, w) }) {
			return false
		}
	}
	return true
}
