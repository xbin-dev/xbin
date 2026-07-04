# The buxon component base rootfs (plans/runtime.md RT-2).
#
# This is NOT a deployment image. buxond overlays it read-only under every
# per-component sandbox and every terminal; a component is layered on top. It
# carries the toolchains + agent CLIs so terminals are a first-class builder
# shell and node/python backends have their runtimes. Go backends are built
# static by buxond, so they need nothing from here.
#
# Build + unpack with hack/build-rootfs.sh (docker build → docker export → dir).
# Customize by FROM-ing this image and adding layers, then point --rootfs at the
# unpacked result — there is no -slim/-fat split to maintain (we only overlay).
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl wget git ripgrep less vim-tiny nano tmux \
      python3 python3-pip python3-venv python-is-python3 \
      build-essential pkg-config passwd sudo \
      iproute2 iputils-ping traceroute dnsutils net-tools \
      procps psmisc lsof \
      jq unzip zip xz-utils file tree \
      htop netcat-openbsd socat rsync openssh-client gnupg \
    && rm -rf /var/lib/apt/lists/*

# Go (full toolchain — components build against it).
ARG GO_VERSION=1.24.0
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xz

# Node LTS.
ARG NODE_VERSION=22.11.0
RUN mkdir -p /usr/local/node \
    && curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz" \
       | tar -C /usr/local/node --strip-components=1 -xJ

# apt normally drops privileges to the `_apt` user for downloads. In a rootless,
# single-uid sandbox namespace only one uid is mapped, so that setuid/setgid
# fails ("setgroups … Operation not permitted") and `apt update`/`install` break.
# Tell apt to run as root instead — the standard fix for unprivileged containers.
# (Terminal overlays are ephemeral, so installs last for the session.)
RUN printf 'APT::Sandbox::User "root";\n' > /etc/apt/apt.conf.d/00buxon-no-sandbox

ENV PATH=/usr/local/go/bin:/usr/local/node/bin:/usr/local/bin:/usr/bin:/bin

# Agent CLIs — so an opened terminal is AI-assisted with zero setup (RT-4).
# (Best-effort: keep the base building even if a registry hiccups.)
RUN npm install -g @anthropic-ai/claude-code opencode-ai || true

# The buxon CLI (built by hack/build-rootfs.sh into the build context).
COPY bx /usr/local/bin/bx
RUN chmod +x /usr/local/bin/bx

# A neutral home for terminals (buxond mounts the real $HOME over it).
RUN useradd -m -s /bin/bash builder || true
