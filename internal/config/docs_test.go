package config_test

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/config"
	"github.com/liquidmetal-dev/muster/internal/tlsconfig"
)

const configurationDoc = "../../docs/configuration.md"

var tlsTypes = []reflect.Type{
	reflect.TypeFor[tlsconfig.Client](),
	reflect.TypeFor[tlsconfig.Server](),
}

// fieldPaths lists the JSON key path of every field under t. A slice of
// structs adds "[]" and a map adds ".<name>". TLS blocks are listed as one
// path because docs/configuration.md describes their fields once.
func fieldPaths(t reflect.Type, prefix string) []string {
	var paths []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		path := prefix + name
		paths = append(paths, path)
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case slices.Contains(tlsTypes, ft):
		case ft.Kind() == reflect.Struct:
			paths = append(paths, fieldPaths(ft, path+".")...)
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
			paths = append(paths, fieldPaths(ft.Elem(), path+"[].")...)
		case ft.Kind() == reflect.Map && ft.Elem().Kind() == reflect.Struct:
			paths = append(paths, fieldPaths(ft.Elem(), path+".<name>.")...)
		}
	}
	return paths
}

func readDoc(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a fixed docs path in the repository
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// section returns the text from the heading line to the next heading of
// the same level.
func section(doc, heading string) string {
	_, rest, ok := strings.Cut(doc, "\n"+heading+"\n")
	if !ok {
		return ""
	}
	level := heading[:strings.IndexByte(heading, ' ')+1]
	if i := strings.Index(rest, "\n"+level); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

func TestConfigurationDocCoversEveryField(t *testing.T) {
	doc := readDoc(t, configurationDoc)

	var missing []string
	for _, p := range fieldPaths(reflect.TypeFor[config.Config](), "") {
		if !strings.Contains(doc, "`"+p+"`") {
			missing = append(missing, p)
		}
	}
	tls := section(doc, "## TLS blocks")
	for _, tt := range tlsTypes {
		for _, p := range fieldPaths(tt, "") {
			if !strings.Contains(tls, "`"+p+"`") {
				missing = append(missing, "TLS blocks: "+p)
			}
		}
	}
	slices.Sort(missing)
	if missing = slices.Compact(missing); len(missing) > 0 {
		t.Errorf("%s does not document:\n  %s", configurationDoc, strings.Join(missing, "\n  "))
	}

	for _, want := range []string{"/api/v3", "one owner", "TLS terminator in front"} {
		if !strings.Contains(doc, want) {
			t.Errorf("%s does not mention %q", configurationDoc, want)
		}
	}
}

// docDuration writes d the way docs/configuration.md does: "10m", "6h",
// "30s", "1m30s".
func docDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func TestDocDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Minute: "10m",
		6 * time.Hour:    "6h",
		30 * time.Second: "30s",
		90 * time.Second: "1m30s",
	} {
		if got := docDuration(d); got != want {
			t.Errorf("docDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestConfigurationDocDefaults(t *testing.T) {
	doc := readDoc(t, configurationDoc)
	defaults := map[string]string{
		"listen.addr":                         config.DefaultListenAddr,
		"api.addr":                            config.DefaultAPIAddr,
		"metrics_addr":                        config.DefaultMetricsAddr,
		"log.format":                          config.DefaultLogFormat,
		"log.level":                           config.DefaultLogLevel,
		"github.api_base_url":                 config.DefaultAPIBaseURL,
		"github.poll.interval":                docDuration(config.DefaultPollInterval),
		"battery.namespace":                   config.DefaultNamespace,
		"shutdown_grace":                      docDuration(config.DefaultShutdownGrace),
		"defaults.claim_deadline":             docDuration(config.DefaultClaimDeadline),
		"defaults.start_timeout":              docDuration(config.DefaultStartTimeout),
		"defaults.idle_timeout":               docDuration(config.DefaultIdleTimeout),
		"defaults.max_session_duration":       docDuration(config.DefaultMaxSessionDuration),
		"defaults.heartbeat_interval":         docDuration(config.DefaultHeartbeatInterval),
		"defaults.heartbeat_expiry_threshold": docDuration(config.DefaultHeartbeatExpiryThreshold),
		"pools[].work_folder":                 config.DefaultWorkFolder,
		"pools[].max_concurrent":              "2 × size",
		"pools[].max_session_duration":        "defaults.max_session_duration",
		"pools[].replenishment_strategy.type": config.StrategyImmediateOnLease,
	}
	for path, want := range defaults {
		row := tableRow(doc, path)
		if row == "" {
			t.Errorf("%s has no table row for `%s`", configurationDoc, path)
			continue
		}
		if !strings.Contains(row, want) {
			t.Errorf("row for `%s` does not give the default %q:\n  %s", path, want, row)
		}
	}
}

// tableRow returns the Markdown table row whose first cell is `path`.
func tableRow(doc, path string) string {
	prefix := fmt.Sprintf("| `%s` |", path)
	for line := range strings.Lines(doc) {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

const quickStartDoc = "../../docs/quick-start.md"

// jsonBlocks returns the body of every fenced code block opened with
// ```json.
func jsonBlocks(doc string) []string {
	var blocks []string
	var b strings.Builder
	in := false
	for line := range strings.Lines(doc) {
		trimmed := strings.TrimSpace(line)
		switch {
		case !in && trimmed == "```json":
			in = true
			b.Reset()
		case in && trimmed == "```":
			in = false
			blocks = append(blocks, b.String())
		case in:
			b.WriteString(line)
		}
	}
	return blocks
}

func TestQuickStartConfigsParse(t *testing.T) {
	blocks := jsonBlocks(readDoc(t, quickStartDoc))
	if len(blocks) != 2 {
		t.Fatalf("%s has %d json blocks, want 2 (webhook and polling configs)", quickStartDoc, len(blocks))
	}
	cfgs := make([]*config.Config, len(blocks))
	for i, block := range blocks {
		cfg, err := config.Parse(strings.NewReader(block))
		if err != nil {
			t.Fatalf("json block %d: Parse() error = %v", i+1, err)
		}
		if len(cfg.Pools) != 1 {
			t.Fatalf("json block %d: %d pools, want 1", i+1, len(cfg.Pools))
		}
		p := cfg.Pools[0]
		if p.Scope.Repo == "" {
			t.Errorf("json block %d: pool scope is not a repository", i+1)
		}
		if !slices.Equal(p.Labels, []string{"self-hosted", "muster"}) {
			t.Errorf("json block %d: labels = %v, want [self-hosted muster]", i+1, p.Labels)
		}
		if p.Template.GetRootVolume().GetSizeInMb() <= 0 {
			t.Errorf("json block %d: microvm_template.rootVolume.sizeInMb is not set", i+1)
		}
		cfgs[i] = cfg
	}
	if cfgs[0].Listen == nil {
		t.Error("json block 1: listen is not set; the first config uses the webhook intake")
	}
	if cfgs[1].Listen != nil || !cfgs[1].GitHub.Poll.Enabled {
		t.Error("json block 2: want listen unset and github.poll.enabled; the second config uses only the polling intake")
	}
}
