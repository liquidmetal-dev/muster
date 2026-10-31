package demand_test

import (
	"testing"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/demand"
)

func testPools() []config.Pool {
	return []config.Pool{
		{Name: "a", Scope: config.Scope{Repo: "acme/a"}, Labels: []string{"self-hosted", "muster", "linux"}},
		{Name: "org", Scope: config.Scope{Org: "acme"}, Labels: []string{"self-hosted", "muster"}},
		{Name: "b", Scope: config.Scope{Repo: "other/b"}, Labels: []string{"self-hosted", "muster"}},
	}
}

func TestMatch(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		repo   string
		want   string
		wantOK bool
	}{
		{"exact labels", []string{"self-hosted", "muster", "linux"}, "acme/a", "a", true},
		{"config order tie-break", []string{"self-hosted", "muster"}, "acme/a", "a", true},
		{"org scope", []string{"self-hosted", "muster"}, "acme/z", "org", true},
		{"case-insensitive", []string{"Self-Hosted", "MUSTER"}, "ACME/A", "a", true},
		{"repo scope", []string{"self-hosted", "muster"}, "other/b", "b", true},
		{"label not in pool", []string{"self-hosted", "muster", "linux"}, "other/b", "", false},
		{"scope exclusion", []string{"self-hosted", "muster"}, "stranger/x", "", false},
		{"hosted label", []string{"ubuntu-latest"}, "acme/a", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := demand.Match(testPools(), tt.labels, tt.repo)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("Match(%v, %q) = %q, %v; want %q, %v", tt.labels, tt.repo, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
