# buxon developer entry points (plans/dev-flow.md).

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: dev dev-noauth dev-plaintext rootfs fuse-overlayfs gocryptfs build test integration vet fmt-check vendor dev-reset

# Dev runs ISOLATED (per-component namespaces + overlay rootfs + egress relay):
# the sandbox network/fs model is different enough from unsandboxed that dev must
# match it. Rootless — no root needed — but requires unprivileged user
# namespaces, and a base rootfs (built once from an OCI image via `make rootfs`).
# Override the rootfs with `ROOTFS=/path make dev`.
ROOTFS ?= $(CURDIR)/.rootfs
FUSE_OVERLAYFS ?= $(CURDIR)/bin/fuse-overlayfs
GOCRYPTFS ?= $(CURDIR)/bin/gocryptfs

# Build the dev/base rootfs (docker → unpacked dir). Rebuilds when the
# Dockerfile or build script change; otherwise cached.
$(ROOTFS)/etc/os-release: docker/rootfs.Dockerfile hack/build-rootfs.sh
	@echo ">> building base rootfs into $(ROOTFS) (needs docker; cached after)"
	rm -rf $(ROOTFS)
	./hack/build-rootfs.sh $(ROOTFS)
rootfs: $(ROOTFS)/etc/os-release

# Our own static fuse-overlayfs (built from source, cached). buxond mounts each
# sandbox root with it so unprivileged directory renames work — i.e. `apt
# install` succeeds in a terminal. Without it buxond falls back to kernel
# overlayfs. It sits in bin/ next to buxond, which finds it automatically.
$(FUSE_OVERLAYFS): hack/build-fuse-overlayfs.sh
	./hack/build-fuse-overlayfs.sh $(CURDIR)/bin
fuse-overlayfs: $(FUSE_OVERLAYFS)

# Our own static gocryptfs (built from source, cached). buxond mounts each
# *encrypted* file-backed resource with it, keyed by the vault barrier, so a
# stolen disk/backup yields only ciphertext (plans/vault-data.md). Sits in bin/
# next to buxond, which finds it automatically.
$(GOCRYPTFS): hack/build-gocryptfs.sh
	./hack/build-gocryptfs.sh $(CURDIR)/bin
gocryptfs: $(GOCRYPTFS)

# The core loop: buxond from source against ./devws, isolated.
# Live-editable core assets + debug logs, with auth ON (multi-user works).
# First run seeds a dev admin: login 'admin' / 'admin'. The token URL is also
# printed for the root admin. Use `make dev-noauth` for admin-everything.
dev: rootfs $(FUSE_OVERLAYFS) $(GOCRYPTFS)
	@mkdir -p devws
	go build -o bin/bx ./cmd/bx   # so terminals have bx on PATH in dev
	BUXON_FUSE_OVERLAYFS=$(FUSE_OVERLAYFS) BUXON_GOCRYPTFS=$(GOCRYPTFS) BUXON_SDK_PATH=$(CURDIR)/sdk go run ./cmd/buxond --dev --isolate --rootfs $(ROOTFS) --workspace ./devws --listen 127.0.0.1:8642

# Frictionless mode: no auth, every request is admin (still isolated).
dev-noauth: rootfs $(FUSE_OVERLAYFS) $(GOCRYPTFS)
	@mkdir -p devws
	go build -o bin/bx ./cmd/bx
	BUXON_FUSE_OVERLAYFS=$(FUSE_OVERLAYFS) BUXON_GOCRYPTFS=$(GOCRYPTFS) BUXON_SDK_PATH=$(CURDIR)/sdk go run ./cmd/buxond --dev --no-auth --isolate --rootfs $(ROOTFS) --workspace ./devws --listen 127.0.0.1:8642

# A bare `make dev` encrypts tile data at rest by default (a built-in dev key;
# plans/vault-data.md) — filesystem/sqlite/blob become gocryptfs mounts and kv
# values are encrypted. `dev-noauth` and `dev-plaintext` stay plaintext (they set
# --no-auth / --insecure-vault) for frictionless work or inspecting on-disk data.
# Start from a fresh ./devws (make dev-reset) when switching modes — plaintext
# sqlite isn't auto-migrated.
dev-plaintext: rootfs $(FUSE_OVERLAYFS) $(GOCRYPTFS)
	@mkdir -p devws
	go build -o bin/bx ./cmd/bx
	BUXON_FUSE_OVERLAYFS=$(FUSE_OVERLAYFS) BUXON_GOCRYPTFS=$(GOCRYPTFS) BUXON_SDK_PATH=$(CURDIR)/sdk go run ./cmd/buxond --dev --insecure-vault --isolate --rootfs $(ROOTFS) --workspace ./devws --listen 127.0.0.1:8642

dev-reset:
	rm -rf devws

build: $(FUSE_OVERLAYFS) $(GOCRYPTFS)
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o bin/buxond ./cmd/buxond
	CGO_ENABLED=0 go build -o bin/bx ./cmd/bx

test:
	go test ./...

integration:
	go test -tags=integration -count=1 -v ./test/...

vet:
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l . | grep -vE '^(devws|\.rootfs)')" || (gofmt -l . | grep -vE '^(devws|\.rootfs)'; echo 'gofmt needed'; exit 1)

vendor:
	./hack/vendor.sh
