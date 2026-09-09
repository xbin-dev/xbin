package boot

import (
	"os"
	"runtime/debug"
	"strings"
)

// BuildVersion resolves the running binary's build id: the -ldflags value
// if one was baked in at build time, else the VCS revision Go stamps into
// the binary automatically (works for `go build`/`go run` from the repo),
// else "dev". This is what the UI shows as the xbind build commit.
func BuildVersion(ldflags string) string {
	if ldflags != "" && ldflags != "dev" {
		return ldflags
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		var rev string
		var dirty bool
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if rev != "" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			if dirty {
				rev += "-dirty"
			}
			return rev
		}
	}
	if ldflags == "" {
		return "dev"
	}
	return ldflags
}

func kernelRelease() string {
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}
