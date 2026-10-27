package version

import (
	"runtime/debug"
	"testing"
)

func fakeInfo(mainVersion string, settings map[string]string) func() (*debug.BuildInfo, bool) {
	bi := &debug.BuildInfo{}
	bi.Main.Version = mainVersion
	for k, v := range settings {
		bi.Settings = append(bi.Settings, debug.BuildSetting{Key: k, Value: v})
	}
	return func() (*debug.BuildInfo, bool) { return bi, true }
}

func TestBuildPrefersLdflags(t *testing.T) {
	got := build(Info{Version: "v1.2.3", Commit: "abcdef1", Date: "2026-10-27T10:00:00Z"}, fakeInfo("(devel)", nil))
	want := Info{Version: "v1.2.3", Commit: "abcdef1", Date: "2026-10-27T10:00:00Z"}
	if got != want {
		t.Fatalf("build() = %+v, want %+v", got, want)
	}
}

func TestBuildFallsBackToBuildInfo(t *testing.T) {
	read := fakeInfo("(devel)", map[string]string{
		"vcs.revision": "0123456789abcdef0123456789abcdef01234567",
		"vcs.time":     "2026-10-27T09:00:00Z",
		"vcs.modified": "false",
	})
	got := build(Info{}, read)
	want := Info{Version: "v0.0.0-dev", Commit: "0123456", Date: "2026-10-27T09:00:00Z"}
	if got != want {
		t.Fatalf("build() = %+v, want %+v", got, want)
	}
}

func TestBuildUsesModuleVersionWhenTagged(t *testing.T) {
	got := build(Info{}, fakeInfo("v0.3.0", nil))
	want := Info{Version: "v0.3.0", Commit: "unknown", Date: "unknown"}
	if got != want {
		t.Fatalf("build() = %+v, want %+v", got, want)
	}
}

func TestBuildMarksDirtyTree(t *testing.T) {
	read := fakeInfo("(devel)", map[string]string{"vcs.revision": "0123456789abcdef", "vcs.modified": "true"})
	if got := build(Info{}, read).Commit; got != "0123456-dirty" {
		t.Fatalf("Commit = %q, want %q", got, "0123456-dirty")
	}
}

func TestBuildWithoutBuildInfo(t *testing.T) {
	got := build(Info{}, func() (*debug.BuildInfo, bool) { return nil, false })
	want := Info{Version: "v0.0.0-dev", Commit: "unknown", Date: "unknown"}
	if got != want {
		t.Fatalf("build() = %+v, want %+v", got, want)
	}
}

func TestInfoString(t *testing.T) {
	s := Info{Version: "v1.2.3", Commit: "abcdef1", Date: "2026-10-27T10:00:00Z"}.String()
	want := "v1.2.3 (commit abcdef1, built 2026-10-27T10:00:00Z)"
	if s != want {
		t.Fatalf("String() = %q, want %q", s, want)
	}
}

func TestGetNeverEmpty(t *testing.T) {
	got := Get()
	if got.Version == "" || got.Commit == "" || got.Date == "" {
		t.Fatalf("Get() has an empty field: %+v", got)
	}
}
