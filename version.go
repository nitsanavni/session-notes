package main

import "runtime/debug"

// version is stamped at build time via:
//
//	-ldflags "-X main.version=$(git describe --tags --always --dirty)"
//
// It is empty for a plain `go build` with no ldflags; versionString then falls
// back to the VCS info Go embeds automatically when building inside the repo.
var version = ""

// versionString returns a human-readable version identifier. It prefers the
// ldflags-stamped value; otherwise it reconstructs one from the build info Go
// records (Go >=1.18 auto-embeds vcs.revision/time/modified for repo builds).
func versionString() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		var rev, at string
		dirty := false
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.time":
				at = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if rev != "" {
			v := "dev-" + rev[:min(12, len(rev))]
			if dirty {
				v += "-dirty"
			}
			if at != "" {
				v += " (" + at + ")"
			}
			return v
		}
	}
	return "unknown"
}
