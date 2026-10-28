package config_test

import (
	"os"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/config"
)

// TestExampleConfigParses keeps docs/examples/musterd.json in step with
// the schema.
func TestExampleConfigParses(t *testing.T) {
	f, err := os.Open("../../docs/examples/musterd.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cfg, err := config.Parse(f)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(cfg.Pools) != 1 || cfg.Pools[0].MaxConcurrent != 6 {
		t.Fatalf("Pools = %+v, want one pool with max_concurrent 6", cfg.Pools)
	}
	if got := cfg.Flintlock.TLSFor("host-a").CAFile; got != "/etc/muster/host-a-ca.pem" {
		t.Fatalf("TLSFor(host-a).CAFile = %q", got)
	}
}
