package github

import (
	"fmt"
	"strings"

	"github.com/liquidmetal-dev/muster/internal/config"
)

// Scope is where runners register: an organization, or one repository
// (requirement GH-2). Exactly one of Org or Owner and Repo is set.
type Scope struct {
	Org   string
	Owner string
	Repo  string
}

// OrgScope returns the organization scope for org.
func OrgScope(org string) Scope { return Scope{Org: org} }

// RepoScope returns the repository scope for an owner/name repository.
func RepoScope(fullName string) (Scope, error) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return Scope{}, fmt.Errorf("%w: repository %q is not owner/name", ErrInvalidRequest, fullName)
	}
	return Scope{Owner: owner, Repo: name}, nil
}

// ScopeFromConfig converts a pool scope from configuration.
func ScopeFromConfig(s config.Scope) (Scope, error) {
	switch {
	case s.Org != "" && s.Repo == "":
		return OrgScope(s.Org), nil
	case s.Org == "" && s.Repo != "":
		return RepoScope(s.Repo)
	default:
		return Scope{}, fmt.Errorf("%w: scope needs exactly one of org or repo", ErrInvalidRequest)
	}
}

// IsOrg reports whether s is an organization scope.
func (s Scope) IsOrg() bool { return s.Org != "" }

// AccountLogin returns the organization, or the repository owner.
func (s Scope) AccountLogin() string {
	if s.IsOrg() {
		return s.Org
	}
	return s.Owner
}

func (s Scope) String() string {
	if s.IsOrg() {
		return "org:" + s.Org
	}
	return "repo:" + s.Owner + "/" + s.Repo
}
