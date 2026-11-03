package github_test

import (
	"errors"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/github"
)

func TestScopeFromConfig(t *testing.T) {
	tests := []struct {
		name    string
		in      config.Scope
		want    github.Scope
		isOrg   bool
		login   string
		str     string
		wantErr error
	}{
		{name: "org", in: config.Scope{Org: "acme"}, want: github.Scope{Org: "acme"}, isOrg: true, login: "acme", str: "org:acme"},
		{name: "repo", in: config.Scope{Repo: "acme/app"}, want: github.Scope{Owner: "acme", Repo: "app"}, login: "acme", str: "repo:acme/app"},
		{name: "bad repo", in: config.Scope{Repo: "bad"}, wantErr: github.ErrInvalidRequest},
		{name: "empty", in: config.Scope{}, wantErr: github.ErrInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := github.ScopeFromConfig(tt.in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ScopeFromConfig: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			if got.IsOrg() != tt.isOrg || got.AccountLogin() != tt.login || got.String() != tt.str {
				t.Fatalf("IsOrg=%v AccountLogin=%q String=%q", got.IsOrg(), got.AccountLogin(), got.String())
			}
		})
	}
}

func TestRepoScopeRejectsMalformed(t *testing.T) {
	for _, in := range []string{"", "acme", "/app", "acme/", "a/b/c"} {
		if _, err := github.RepoScope(in); !errors.Is(err, github.ErrInvalidRequest) {
			t.Errorf("RepoScope(%q) err = %v, want ErrInvalidRequest", in, err)
		}
	}
}
