# The xbin component base rootfs (plans/runtime.md RT-2).
#
# This is NOT a deployment image. xbind overlays it read-only under every
# per-component sandbox and every terminal; a component is layered on top. It
# carries the toolchains + agent CLIs so terminals are a first-class builder
# shell and node/python backends have their runtimes. Go backends are built
# static by xbind, so they need nothing from here.
#
# Build + unpack with hack/build-rootfs.sh (docker build → docker export → dir).
# Customize by FROM-ing this image and adding layers, then point --rootfs at the
# unpacked result — there is no -slim/-fat split to maintain (we only overlay).
FROM docker.io/library/ubuntu:26.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl wget git ripgrep less vim nano tmux \
      python3 python3-pip python3-venv python-is-python3 \
      build-essential pkg-config passwd sudo \
      iproute2 iputils-ping traceroute dnsutils net-tools \
      procps psmisc lsof strace \
      jq unzip zip xz-utils file tree \
      htop netcat-openbsd socat rsync openssh-client gnupg \
      fd-find bat shellcheck zsh \
    && rm -rf /var/lib/apt/lists/*

# Go (full toolchain — components build against it).
ARG GO_VERSION=1.24.0
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xz

# Node LTS. Keep this fresh within the LTS line: current pnpm requires
# >=22.13, and an old pin ships a silently broken pnpm (2026-08-07 find).
ARG NODE_VERSION=22.23.2
RUN mkdir -p /usr/local/node \
    && curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz" \
       | tar -C /usr/local/node --strip-components=1 -xJ

# Bun — the package installer for the JS tools below (~10x faster than npm:
# claude-code installs in <1s vs ~10s) and a first-class runtime builders
# reach for anyway. Global installs land in $BUN_INSTALL/bin (also wired into
# xbind's sandbox PATHs — internal/term, internal/runner/env.go).
ARG BUN_VERSION=1.3.14
ENV BUN_INSTALL=/usr/local/bun
RUN curl -fsSL "https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/bun-linux-x64.zip" -o /tmp/bun.zip \
    && unzip -q /tmp/bun.zip -d /tmp \
    && install -m755 /tmp/bun-linux-x64/bun /usr/local/bin/bun \
    && rm -rf /tmp/bun.zip /tmp/bun-linux-x64

# apt normally drops privileges to the `_apt` user for downloads. In a rootless,
# single-uid sandbox namespace only one uid is mapped, so that setuid/setgid
# fails ("setgroups … Operation not permitted") and `apt update`/`install` break.
# Tell apt to run as root instead — the standard fix for unprivileged containers.
#
# apt's `partial/ → parent` rename (both /var/cache/apt/archives and
# /var/lib/apt/lists) fails with EXDEV ("Invalid cross-device link") on some
# kernels when those dirs live on fuse-overlayfs. We do NOT try to dodge that by
# relocating the dirs (a base-absent path doesn't change fuse-overlayfs's
# cross-layer rename behavior — it was tried and regressed `apt update`).
# Instead xbind mounts a real tmpfs over both dirs in the terminal sandbox, so
# the rename happens on a normal filesystem and can never cross devices
# (internal/term; docs/isolation.md). Nothing to configure here beyond the
# sandbox-user fix; apt keeps its default paths.
RUN printf 'APT::Sandbox::User "root";\n' > /etc/apt/apt.conf.d/00xbin-no-sandbox

# (/usr/sbin included: useradd lives there — without it the builder-user
# step below failed silently under its || true for every past build.)
ENV PATH=/usr/local/go/bin:/usr/local/node/bin:/usr/local/bun/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# JS package managers every frontend agent reaches for. Via bun, NOT corepack
# (this node's bundled corepack has stale signing keys and rejects current
# pnpm/yarn — and its shims would hit the same failure on first use).
RUN bun add -g pnpm yarn || true

# Agent CLIs — so an opened terminal is AI-assisted with zero setup (RT-4).
# ONE TOOL PER STEP: a single shared npm invocation was a transaction — when
# opencode's postinstall broke, npm rolled back claude and codex with it, and
# `|| true` shipped a green image missing all three (2026-08-07). Keep each
# best-effort (a registry hiccup must not brick base builds — the inventory
# step at the end reports anything missing, loudly).
RUN bun add -g @anthropic-ai/claude-code || true
RUN bun add -g @openai/codex || true

# opencode from its release binary (same asset its official installer uses),
# NOT npm: its npm postinstall re-invokes npm with the parent's lifecycle env
# (npm_config_global etc.) and misplaces the platform binary, and npm treats
# a failed optionalDependency as a silent skip — both bit us. A pinned glibc
# binary sidesteps every layer of that. Bump the pin to update.
ARG OPENCODE_VERSION=1.18.15
RUN curl -fsSL "https://github.com/anomalyco/opencode/releases/download/v${OPENCODE_VERSION}/opencode-linux-x64.tar.gz" \
       | tar -xz -C /usr/local/bin opencode \
    && chmod 755 /usr/local/bin/opencode || true

# Go dev tools every Go agent installs, into a system GOBIN on PATH (each
# terminal has its own GOPATH, so a plain `go install` wouldn't be shared).
RUN GOBIN=/usr/local/bin GOFLAGS=-mod=mod go install golang.org/x/tools/gopls@latest || true \
    && GOBIN=/usr/local/bin go install github.com/go-delve/delve/cmd/dlv@latest || true \
    && curl -fsSL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
       | sh -s -- -b /usr/local/bin || true

# GitHub CLI (official apt repo).
RUN mkdir -p -m 755 /etc/apt/keyrings \
    && wget -qO- https://cli.github.com/packages/githubcli-archive-keyring.gpg > /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh && rm -rf /var/lib/apt/lists/* || true

# Chromium + Playwright for frontend / e2e agents. Browsers land in a system
# path (terminals run under a different $HOME, so the default ~/.cache wouldn't
# be found), and --with-deps pulls chromium's system libraries.
ENV PLAYWRIGHT_BROWSERS_PATH=/usr/local/ms-playwright
RUN npm install -g playwright @playwright/test || true \
    && (playwright install --with-deps chromium || true)

# Ship no build-time .debs or package index lists (the terminal tmpfs mounts
# start these dirs empty at runtime anyway; keep the image lean).
RUN apt-get clean && rm -rf /var/cache/apt/archives/* /var/lib/apt/lists/*

# The xbin CLI (built by hack/build-rootfs.sh into the build context).
COPY bx /usr/local/bin/bx
RUN chmod +x /usr/local/bin/bx

# A neutral home for terminals (xbind mounts the real $HOME over it).
RUN useradd -m -s /bin/bash builder || true

# Tool inventory — the last word in the build log. Steps above are
# best-effort by design; this makes anything missing IMPOSSIBLE to miss and
# stamps the manifest into the image for doctor-style checks. The build
# stays green (see the per-step comments), the log does not stay quiet.
RUN set -- node bun go claude codex opencode pnpm yarn npm gopls dlv gh bx playwright; \
    ok=1; : > /etc/xbin-rootfs-tools; \
    for t in "$@"; do \
      if command -v "$t" >/dev/null 2>&1; then \
        printf '%s\n' "$t" >> /etc/xbin-rootfs-tools; \
      else \
        echo "!! ROOTFS MISSING TOOL: $t" >&2; ok=0; \
      fi; \
    done; \
    [ $ok = 1 ] && echo ">> rootfs tool inventory complete: $*" || echo ">> rootfs built WITH MISSING TOOLS (see above)"
