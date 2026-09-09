package main

import (
	"strings"
	"testing"
)

// The two renderers are the tile's whole output: traefik's static config
// (entry points, the file provider, ACME) and the per-host dynamic routers.
// Pinned exactly, so a change to what traefik is told is a deliberate diff.
func TestStaticConfig(t *testing.T) {
	got := staticConfig("/data", settings{Email: "ops@example.com", Staging: true})
	want := `[entryPoints]
  [entryPoints.web]
    address = ":80"
  [entryPoints.websecure]
    address = ":443"

[providers]
  [providers.file]
    directory = "/data/dynamic"
    watch = true

[log]
  level = "INFO"

[ping]
  entryPoint = "web"

[certificatesResolvers.le.acme]
  email = "ops@example.com"
  storage = "/data/acme.json"
  caServer = "https://acme-staging-v02.api.letsencrypt.org/directory"
  [certificatesResolvers.le.acme.tlsChallenge]
`
	if got != want {
		t.Errorf("staticConfig (staging):\n%s\nwant:\n%s", got, want)
	}
	prod := staticConfig("/data", settings{Email: "ops@example.com"})
	if !strings.Contains(prod, `caServer = "https://acme-v02.api.letsencrypt.org/directory"`) {
		t.Errorf("production ACME endpoint missing:\n%s", prod)
	}
	plain := staticConfig("/data", settings{NoTLS: true})
	if strings.Contains(plain, "certificatesResolvers") {
		t.Errorf("--noTls must not configure ACME:\n%s", plain)
	}
}

func TestDynamicConfig(t *testing.T) {
	routes := []route{{Host: "a.example.com", Component: "apps/a", Slot: "web"}, {Host: "b.example.com", Component: "apps/b", Slot: "web"}}
	got := dynamicConfig(routes, settings{}, "http://127.0.0.1:8080")
	want := "[http.services.xbin.loadBalancer]\n" +
		"  [[http.services.xbin.loadBalancer.servers]]\n    url = \"http://127.0.0.1:8080\"\n\n" +
		"[http.routers.r0]\n  rule = \"Host(`a.example.com`)\"\n  entryPoints = [\"websecure\"]\n  service = \"xbin\"\n  [http.routers.r0.tls]\n    certResolver = \"le\"\n\n" +
		"[http.routers.r0_http]\n  rule = \"Host(`a.example.com`)\"\n  entryPoints = [\"web\"]\n  service = \"xbin\"\n  middlewares = [\"to-https\"]\n\n" +
		"[http.routers.r1]\n  rule = \"Host(`b.example.com`)\"\n  entryPoints = [\"websecure\"]\n  service = \"xbin\"\n  [http.routers.r1.tls]\n    certResolver = \"le\"\n\n" +
		"[http.routers.r1_http]\n  rule = \"Host(`b.example.com`)\"\n  entryPoints = [\"web\"]\n  service = \"xbin\"\n  middlewares = [\"to-https\"]\n\n" +
		"[http.middlewares.to-https.redirectScheme]\n  scheme = \"https\"\n  permanent = true\n"
	if got != want {
		t.Errorf("dynamicConfig (tls):\n%s\nwant:\n%s", got, want)
	}
	plain := dynamicConfig(routes[:1], settings{NoTLS: true}, "http://127.0.0.1:8080")
	if strings.Contains(plain, "websecure") || strings.Contains(plain, "to-https") || !strings.Contains(plain, "entryPoints = [\"web\"]") {
		t.Errorf("--noTls must route :80 only, no redirect:\n%s", plain)
	}
	if empty := dynamicConfig(nil, settings{}, "http://x"); strings.Contains(empty, "to-https") {
		t.Errorf("no routes ⇒ no redirect middleware:\n%s", empty)
	}
}
