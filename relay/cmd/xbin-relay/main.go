// Command xbin-relay runs the push relay (relay/README.md).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/relay"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// rateFlag parses "PER_HOUR/BURST" (e.g. "10/3"); "" keeps the default.
func rateFlag(name, v string) (relay.Rate, error) {
	if v == "" {
		return relay.Rate{}, nil
	}
	a, b, ok := strings.Cut(v, "/")
	ph, err1 := strconv.ParseFloat(a, 64)
	burst, err2 := strconv.ParseFloat(b, 64)
	if !ok || err1 != nil || err2 != nil || ph <= 0 || burst < 1 {
		return relay.Rate{}, fmt.Errorf("-%s %q: want PER_HOUR/BURST, e.g. 10/3", name, v)
	}
	return relay.Rate{PerHour: ph, Burst: burst}, nil
}

func durEnv(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func intEnv(name string) int {
	n, _ := strconv.Atoi(os.Getenv(name))
	return n
}

func main() {
	var (
		listen     = flag.String("listen", env("XBIN_RELAY_LISTEN", "127.0.0.1:8650"), "listen address")
		state      = flag.String("state", env("XBIN_RELAY_STATE", "relay-state.json"), "state file (handles, workspace key hashes)")
		keyPath    = flag.String("apns-key", env("XBIN_RELAY_APNS_KEY", ""), "APNs auth key (.p8); empty = no delivery (503)")
		keyID      = flag.String("apns-key-id", env("XBIN_RELAY_APNS_KEY_ID", ""), "the .p8 key's id")
		teamID     = flag.String("apns-team-id", env("XBIN_RELAY_APNS_TEAM_ID", ""), "the Apple developer team id")
		topics     = flag.String("topics", env("XBIN_RELAY_TOPICS", ""), "comma-separated app bundle ids handles may name (empty = any; development only)")
		trustProxy = flag.Bool("trust-proxy", os.Getenv("XBIN_RELAY_TRUST_PROXY") != "", "take client IPs from the last X-Forwarded-For hop")
		tlsCert    = flag.String("tls-cert", env("XBIN_RELAY_TLS_CERT", ""), "TLS certificate (PEM); empty = plain HTTP behind a TLS proxy")
		tlsKey     = flag.String("tls-key", env("XBIN_RELAY_TLS_KEY", ""), "TLS key (PEM)")
		verify     = flag.Bool("verify-tokens", os.Getenv("XBIN_RELAY_NO_VERIFY_TOKENS") == "", "check each device token with APNs before storing a handle (needs -apns-key)")
		regTokens  = flag.String("registration-tokens", env("XBIN_RELAY_REGISTRATION_TOKENS", ""), "comma-separated tokens POST /v1/workspaces then requires (Authorization: Bearer); empty = open registration")
		maxWS      = flag.Int("max-workspaces", intEnv("XBIN_RELAY_MAX_WORKSPACES"), "workspaces at most (0 = 200000)")
		maxH       = flag.Int("max-handles", intEnv("XBIN_RELAY_MAX_HANDLES"), "handles at most (0 = 2000000)")
		unusedWS   = flag.Duration("unused-workspace-ttl", durEnv("XBIN_RELAY_UNUSED_WORKSPACE_TTL", 0), "delete a workspace key never used after this (0 = 720h)")
		idleWS     = flag.Duration("idle-workspace-ttl", durEnv("XBIN_RELAY_IDLE_WORKSPACE_TTL", 0), "delete a workspace key unused (no push, no key check) for this long (0 = 4320h)")
		unboundH   = flag.Duration("unbound-handle-ttl", durEnv("XBIN_RELAY_UNBOUND_HANDLE_TTL", 0), "delete a handle no workspace pushed to after this (0 = 720h)")
		idleH      = flag.Duration("idle-handle-ttl", durEnv("XBIN_RELAY_IDLE_HANDLE_TTL", 0), "delete a handle nothing pushed to for this long (0 = 4320h)")
		rates      = map[string]*string{}
	)
	for _, r := range []struct{ name, env, def string }{
		{"workspace-rate", "XBIN_RELAY_WORKSPACE_RATE", "pushes per workspace (3600/120)"},
		{"handle-rate", "XBIN_RELAY_HANDLE_RATE", "pushes per handle (600/30)"},
		{"refused-push-rate", "XBIN_RELAY_REFUSED_PUSH_RATE", "pushes to unknown or foreign handles per workspace (600/60)"},
		{"new-workspace-rate", "XBIN_RELAY_NEW_WORKSPACE_RATE", "workspace registrations per client (10/3)"},
		{"all-new-workspaces-rate", "XBIN_RELAY_ALL_NEW_WORKSPACES_RATE", "workspace registrations from everyone together (600/60)"},
		{"new-handle-rate", "XBIN_RELAY_NEW_HANDLE_RATE", "handle registrations per client (360/60)"},
	} {
		rates[r.name] = flag.String(r.name, env(r.env, ""), "PER_HOUR/BURST: "+r.def+"; empty = the default")
	}
	flag.Parse()
	cfg := relay.Config{StatePath: *state, TrustProxy: *trustProxy, VerifyTokens: *verify,
		MaxWorkspaces: *maxWS, MaxHandles: *maxH, UnusedWorkspaceTTL: *unusedWS, IdleWorkspaceTTL: *idleWS,
		UnboundHandleTTL: *unboundH, IdleHandleTTL: *idleH}
	var err error
	for name, dst := range map[string]*relay.Rate{"workspace-rate": &cfg.WorkspaceRate, "handle-rate": &cfg.HandleRate,
		"refused-push-rate": &cfg.RefusedPushRate, "new-workspace-rate": &cfg.NewWorkspaceRate,
		"all-new-workspaces-rate": &cfg.AllNewWorkspacesRate, "new-handle-rate": &cfg.NewHandleRate} {
		if *dst, err = rateFlag(name, *rates[name]); err != nil {
			fmt.Fprintln(os.Stderr, "xbin-relay:", err)
			os.Exit(2)
		}
	}
	for _, t := range strings.Split(*regTokens, ",") {
		if t = strings.TrimSpace(t); t != "" {
			cfg.RegistrationTokens = append(cfg.RegistrationTokens, t)
		}
	}
	if err := run(cfg, *listen, *keyPath, *keyID, *teamID, *topics, *tlsCert, *tlsKey); err != nil {
		fmt.Fprintln(os.Stderr, "xbin-relay:", err)
		os.Exit(1)
	}
}

func run(cfg relay.Config, listen, keyPath, keyID, teamID, topics, tlsCert, tlsKey string) error {
	for _, t := range strings.Split(topics, ",") {
		if t = strings.TrimSpace(t); t != "" {
			cfg.Topics = append(cfg.Topics, t)
		}
	}
	if keyPath != "" {
		pemBytes, err := os.ReadFile(keyPath)
		if err != nil {
			return err
		}
		k, err := relay.LoadP8(pemBytes)
		if err != nil {
			return err
		}
		if keyID == "" || teamID == "" {
			return errors.New("-apns-key needs -apns-key-id and -apns-team-id")
		}
		cfg.APNs = &relay.APNs{KeyID: keyID, TeamID: teamID, Key: k, Client: &http.Client{Timeout: 20 * time.Second}}
	} else {
		slog.Warn("no APNs key: pushes are refused with 503 (registration still works)")
	}
	if len(cfg.Topics) == 0 {
		slog.Warn("no -topics: handles may name any app (development only)")
	}
	s, err := relay.New(cfg)
	if err != nil {
		return err
	}
	h, w := s.Counts()
	slog.Info("xbin-relay listening", "addr", listen, "handles", h, "workspaces", w, "apns", cfg.APNs != nil)
	srv := &http.Server{Addr: listen, Handler: s, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		if tlsCert != "" {
			errc <- srv.ListenAndServeTLS(tlsCert, tlsKey)
		} else {
			errc <- srv.ListenAndServe()
		}
	}()
	select {
	case err = <-errc:
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = srv.Shutdown(sctx)
	}
	// fold the journal into the snapshot (usage timestamps included)
	return errors.Join(err, s.Close())
}
