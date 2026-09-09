// xbind — the xbin workspace daemon. See plans/ for the design and /docs/
// (served by this binary) for the builder-facing documentation.
//
//	xbind init <dir>              scaffold a workspace
//	xbind [flags]                 serve a workspace
//	xbind version
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

func fatal(f string, args ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", args...)
	os.Exit(1)
}
