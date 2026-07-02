# buxon — fat workstation-appliance image (decision D1).
# Toolchains for every supported runtime are baked in; the workspace bind
# mount at /workspace is the only state. ~2.5–3 GB and that's fine.

# --- build buxond + bx ---
FROM golang:1.25-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION}" -o /out/buxond ./cmd/buxond \
 && CGO_ENABLED=0 go build -o /out/bx ./cmd/bx

# --- runtime ---
FROM debian:trixie-slim

# Terminal + build environment for workspace shells and component backends.
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates curl git jq less nano vim-tiny ripgrep tmux openssh-client \
    build-essential pkg-config \
    python3 python3-venv \
    nodejs npm \
    sqlite3 \
 && rm -rf /var/lib/apt/lists/*

# Full Go toolchain (components are compiled inside the container).
COPY --from=build /usr/local/go /usr/local/go

COPY --from=build /out/buxond /out/bx /opt/buxon/bin/
COPY sdk /opt/buxon/sdk

ENV PATH=/opt/buxon/bin:/usr/local/go/bin:$PATH \
    BUXON_WORKSPACE=/workspace \
    BUXON_LISTEN=0.0.0.0:8642 \
    BUXON_SDK_PATH=/opt/buxon/sdk \
    SHELL=/bin/bash

VOLUME /workspace
EXPOSE 8642

# buxond is PID 1: reaps zombies, forwards signals, and (D13b) drops to the
# workspace owner's uid — or keeps root when started with --scope-uids.
ENTRYPOINT ["buxond"]
