// Package version reports the build version of the muster binaries.
//
// Release builds set the three variables with the linker:
//
//	-ldflags "-X github.com/liquidmetal-dev/muster/internal/version.Version=v1.2.3
//	          -X github.com/liquidmetal-dev/muster/internal/version.Commit=abcdef1
//	          -X github.com/liquidmetal-dev/muster/internal/version.Date=2026-10-27T10:00:00Z"
//
// A plain `go build` leaves them empty, and Get falls back to the values that
// the Go toolchain records in the binary.
package version

import (
	"fmt"
	"runtime/debug"
)

// Set by the linker in release builds. See the package documentation.
var (
	Version string
	Commit  string
	Date    string
)

const (
	devVersion = "v0.0.0-dev"
	unknown    = "unknown"
	shortSHA   = 7
)

// Info is the version of the running binary.
type Info struct {
	Version string
	Commit  string
	Date    string
}

// String formats the version as "<Version> (commit <Commit>, built <Date>)".
func (i Info) String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", i.Version, i.Commit, i.Date)
}

// Get returns the version of the running binary. Linker-set values win field
// by field; the rest comes from the build information in the binary.
func Get() Info {
	return build(Info{Version: Version, Commit: Commit, Date: Date}, debug.ReadBuildInfo)
}

// build fills the empty fields of overrides from the build information that
// read returns.
func build(overrides Info, read func() (*debug.BuildInfo, bool)) Info {
	info := overrides
	bi, ok := read()
	if !ok || bi == nil {
		bi = &debug.BuildInfo{}
	}
	settings := make(map[string]string, len(bi.Settings))
	for _, s := range bi.Settings {
		settings[s.Key] = s.Value
	}

	if info.Version == "" {
		info.Version = bi.Main.Version
		if info.Version == "" || info.Version == "(devel)" {
			info.Version = devVersion
		}
	}
	if info.Commit == "" {
		info.Commit = unknown
		if rev := settings["vcs.revision"]; rev != "" {
			if len(rev) > shortSHA {
				rev = rev[:shortSHA]
			}
			if settings["vcs.modified"] == "true" {
				rev += "-dirty"
			}
			info.Commit = rev
		}
	}
	if info.Date == "" {
		info.Date = unknown
		if t := settings["vcs.time"]; t != "" {
			info.Date = t
		}
	}
	return info
}
