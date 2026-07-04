# buxon developer entry points (plans/dev-flow.md).

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: dev dev-noauth rootfs fuse-overlayfs build test integration vet fmt-check vendor image dev-reset

# Dev runs ISOLATED (per-component namespaces + overlay rootfs + egress relay):
# the sandbox network/fs model is different enough from unsandboxed that dev must
# match it. Rootless — no root needed — but requires unprivileged user
# namespaces, and a base rootfs (built once from an OCI image via `make rootfs`).
# Override the rootfs with `ROOTFS=/path make dev`.
ROOTFS ?= $(CURDIR)/.rootfs
FUSE_OVERLAYFS ?= $(CURDIR)/bin/fuse-overlayfs

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

# The core loop: buxond from source against ./devws, isolated.
# Live-editable core assets + debug logs, with auth ON (multi-user works).
# First run seeds a dev admin: login 'admin' / 'admin'. The token URL is also
# printed for the root admin. Use `make dev-noauth` for admin-everything.
dev: rootfs $(FUSE_OVERLAYFS)
	@mkdir -p devws
	go build -o bin/bx ./cmd/bx   # so terminals have bx on PATH in dev
	BUXON_FUSE_OVERLAYFS=$(FUSE_OVERLAYFS) BUXON_SDK_PATH=$(CURDIR)/sdk go run ./cmd/buxond --dev --isolate --rootfs $(ROOTFS) --workspace ./devws --listen 127.0.0.1:8642

# Frictionless mode: no auth, every request is admin (still isolated).
dev-noauth: rootfs $(FUSE_OVERLAYFS)
	@mkdir -p devws
	go build -o bin/bx ./cmd/bx
	BUXON_FUSE_OVERLAYFS=$(FUSE_OVERLAYFS) BUXON_SDK_PATH=$(CURDIR)/sdk go run ./cmd/buxond --dev --no-auth --isolate --rootfs $(ROOTFS) --workspace ./devws --listen 127.0.0.1:8642

dev-reset:
	rm -rf devws

build: $(FUSE_OVERLAYFS)
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

image:
	docker build -f docker/Dockerfile --build-arg VERSION=$(VERSION) -t buxon:$(VERSION) .
