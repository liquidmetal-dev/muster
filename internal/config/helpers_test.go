package config_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/config"
)

// fixture returns testdata/valid.json decoded into a generic map.
func fixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/valid.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return m
}

// parseMutated applies mutate to the fixture, re-encodes it and parses it.
func parseMutated(t *testing.T, mutate func(m map[string]any)) (*config.Config, error) {
	t.Helper()
	m := fixture(t)
	if mutate != nil {
		mutate(m)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return config.Parse(bytes.NewReader(raw))
}

// pool returns pools[i] of the generic fixture.
func pool(m map[string]any, i int) map[string]any {
	return m["pools"].([]any)[i].(map[string]any)
}

// sub returns the nested object at a dotted path, creating missing levels.
func sub(m map[string]any, path string) map[string]any {
	cur := m
	for _, k := range strings.Split(path, ".") {
		next, ok := cur[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[k] = next
		}
		cur = next
	}
	return cur
}

// set assigns v at a dotted path such as "github.poll.interval".
func set(m map[string]any, path string, v any) {
	i := strings.LastIndex(path, ".")
	if i < 0 {
		m[path] = v
		return
	}
	sub(m, path[:i])[path[i+1:]] = v
}

// del removes the key at a dotted path.
func del(m map[string]any, path string) {
	i := strings.LastIndex(path, ".")
	if i < 0 {
		delete(m, path)
		return
	}
	delete(sub(m, path[:i]), path[i+1:])
}

// wantErr asserts that err is non-nil and contains want.
func wantErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want one containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want one containing %q", err.Error(), want)
	}
}
