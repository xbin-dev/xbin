# buxon developer entry points (plans/dev-flow.md).

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: dev dev-noauth build test integration vet fmt-check vendor image dev-reset

# The core loop: buxond from source against ./devws.
# Live-editable core assets + debug logs, with auth ON (multi-user works).
# First run seeds a dev admin: login 'admin' / 'admin'. The token URL is also
# printed for the root admin. Use `make dev-noauth` for admin-everything.
dev:
	@mkdir -p devws
	go build -o bin/bx ./cmd/bx   # so terminals have bx on PATH in dev
	BUXON_SDK_PATH=$(CURDIR)/sdk go run ./cmd/buxond --dev --workspace ./devws --listen 127.0.0.1:8642

# Frictionless mode: no auth, every request is admin (the old `make dev`).
dev-noauth:
	@mkdir -p devws
	go build -o bin/bx ./cmd/bx
	BUXON_SDK_PATH=$(CURDIR)/sdk go run ./cmd/buxond --dev --no-auth --workspace ./devws --listen 127.0.0.1:8642

dev-reset:
	rm -rf devws

build:
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o bin/buxond ./cmd/buxond
	CGO_ENABLED=0 go build -o bin/bx ./cmd/bx

test:
	go test ./...

integration:
	go test -tags=integration -count=1 -v ./test/...

vet:
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l . | grep -v ^devws)" || (gofmt -l . | grep -v ^devws; echo 'gofmt needed'; exit 1)

vendor:
	./hack/vendor.sh

image:
	docker build -f docker/Dockerfile --build-arg VERSION=$(VERSION) -t buxon:$(VERSION) .
