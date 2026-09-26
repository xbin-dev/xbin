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
	)
	flag.Parse()
	if err := run(*listen, *state, *keyPath, *keyID, *teamID, *topics, *trustProxy, *tlsCert, *tlsKey, *verify); err != nil {
		fmt.Fprintln(os.Stderr, "xbin-relay:", err)
		os.Exit(1)
	}
}

func run(listen, state, keyPath, keyID, teamID, topics string, trustProxy bool, tlsCert, tlsKey string, verify bool) error {
	cfg := relay.Config{StatePath: state, TrustProxy: trustProxy, VerifyTokens: verify}
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
