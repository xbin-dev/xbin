// xbind — the xbin workspace daemon. See plans/ for the design and /docs/
// (served by this binary) for the builder-facing documentation.
//
//	xbind init <dir>              scaffold a workspace
//	xbind [flags]                 serve a workspace
//	xbind version
//	xbind sealed-build [flags]    build the release's sealed backends (make build)
//
// Everything after the command line is internal/boot: Run(ctx, cfg) boots
// the workspace and serves it until the context ends; this file owns the
// flags, the log level, the signals and the exit code.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/xbin-dev/xbin/internal/boot"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sealed"
)

var version = "dev" // set via -ldflags at release (make build → `git describe`)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "__sandbox-init":
			// Hidden re-exec target: run as PID 1 inside a component's fresh
			// namespaces, assemble its rootfs, and exec the backend. Must be
			// handled before any flag parsing (plans/isolation-impl.md).
			if len(os.Args) < 3 {
				fatal("usage: xbind __sandbox-init <spec>")
			}
			sandbox.RunInit(os.Args[2]) // never returns on linux
			return
		case "init":
			if len(os.Args) < 3 {
				fatal("usage: xbind init <dir>")
			}
			if err := boot.InitWorkspace(os.Args[2]); err != nil {
				fatal("init: %v", err)
			}
			fmt.Println("workspace initialized:", os.Args[2])
			return
		case "version":
			fmt.Println("xbind", boot.BuildVersion(version))
			return
		case "sealed-build":
			// The release's builds of its sealed components' backends
			// (docs/maintenance.md → Sealed components): `make build` runs
			// this with the host's go for the target it builds.
			if err := sealedBuild(os.Args[2:]); err != nil {
				fatal("sealed-build: %v", err)
			}
			return
		}
	}

	cfg := &boot.Config{}
	cfg.RegisterFlags(flag.CommandLine)
	flag.Parse()
	cfg.FromEnv()
	cfg.Version = boot.BuildVersion(version) // resolve once for the daemon (brk + server + gateway)

	// --dev serves web/docs from the source tree and turns on debug logs; it
	// no longer implies --no-auth, so `make dev` can exercise multi-user auth
	// while live-editing core elements. Use --no-auth explicitly (or
	// `make dev-noauth`) for the frictionless admin-everything mode.
	lvl := slog.LevelInfo
	if cfg.Dev {
		lvl = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := boot.Run(ctx, cfg); err != nil {
		fatal("%v", err)
	}
}

// sealedBuild is `xbind sealed-build`: compile the backend of every Go
// component of the compiled-in table into --out as <name>-<digest>, for
// --os/--arch (default: the go command's), and remove what else is there;
// --list prints the file names a release's bin/sealed must hold instead,
// --check verifies --out holds exactly those.
func sealedBuild(args []string) error {
	fs := flag.NewFlagSet("sealed-build", flag.ContinueOnError)
	out := fs.String("out", "bin/sealed", "the directory the backends are built into")
	sdk := fs.String("sdk", "sdk", "the xbin Go SDK's directory")
	goos := fs.String("os", "", "target GOOS (default: the go command's)")
	goarch := fs.String("arch", "", "target GOARCH (default: the go command's)")
	list := fs.Bool("list", false, "print the file names bin/sealed must hold, and build nothing")
	check := fs.Bool("check", false, "verify --out holds exactly those files, and build nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	set, err := sealed.Load(sealed.Table())
	if err != nil {
		return err
	}
	want := set.BuildNames()
	switch {
	case *list:
		for _, n := range want {
			fmt.Println(n)
		}
		return nil
	case *check:
		ents, err := os.ReadDir(*out)
		if err != nil && (len(want) > 0 || !os.IsNotExist(err)) {
			return err
		}
		have := map[string]bool{}
		for _, e := range ents {
			have[e.Name()] = true
		}
		for _, n := range want {
			if !have[n] {
				return fmt.Errorf("%s has no %s: the release would hold that component (no-build)", *out, n)
			}
			delete(have, n)
		}
		for n := range have {
			return fmt.Errorf("%s holds %s, which isn't a build of this source", *out, n)
		}
		fmt.Printf("sealed-build: %s holds this source's %d backend(s)\n", *out, len(want))
		return nil
	}
	have, err := set.Build(sealed.BuildOptions{Out: *out, SDK: *sdk, GOOS: *goos, GOARCH: *goarch, Log: os.Stderr})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "sealed-build: %s holds %d backend(s)\n", *out, len(have))
	return nil
}

func fatal(f string, args ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", args...)
	os.Exit(1)
}
