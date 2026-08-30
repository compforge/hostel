# Copyright 2026 Li Qiankun
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Multi-arch (linux/amd64, linux/arm64). hostel is pure Go, so the builder runs
# NATIVELY on the build host ($BUILDPLATFORM) and cross-compiles for the target
# arch ($TARGETARCH) — no QEMU emulation of the Go toolchain. The bwrap builder
# and runtime stages run as $TARGETPLATFORM so native dependencies match it.
ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH
ARG BWRAP_VERSION=0.11.2
ARG BWRAP_SHA256=69abc30005d2186baf7737feacd8da35633b93cf5af38838ecff17c5f8e924f6
ARG PATHSHIM_REV=7ac72a8e10a324e6dff7e7c24233bc0c9105904a

# --- build stage: static, pure-Go hostel binary --------------------------
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder
ARG TARGETOS
ARG TARGETARCH
# GOPROXY overridable for networks without proxy.golang.org (e.g. corp mirrors).
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY . .
ARG VERSION
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    VERSION="${VERSION:-$(cat VERSION)}" && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/hostel ./cmd/hostel

# --- pathshim stage: pinned best-effort dorm/room workspace view --------
# This stage runs on the target platform, like bwrap, so its libc and
# architecture match the runtime image. The revision is pinned because
# pathshim does not publish release artifacts yet.
FROM rust:1.82-bookworm AS pathshim-builder
ARG PATHSHIM_REV
RUN apt-get update && apt-get install -y --no-install-recommends git \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src/pathshim
RUN git init \
    && git remote add origin https://github.com/compforge/pathshim.git \
    && git fetch --depth 1 origin "${PATHSHIM_REV}" \
    && git checkout --detach FETCH_HEAD
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/pathshim/target \
    cargo build --release --locked \
    && cp target/release/pathshim /tmp/pathshim

# --- bubblewrap stage: pinned non-setuid suite backend -------------------
FROM debian:bookworm-slim AS bwrap-builder
ARG BWRAP_VERSION
ARG BWRAP_SHA256
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential ca-certificates curl libcap-dev meson ninja-build \
        pkg-config xz-utils \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src/bubblewrap
RUN curl -fsSL -o bubblewrap.tar.xz \
        "https://github.com/containers/bubblewrap/releases/download/v${BWRAP_VERSION}/bubblewrap-${BWRAP_VERSION}.tar.xz" \
    && echo "${BWRAP_SHA256} *bubblewrap.tar.xz" | sha256sum -c - \
    && tar -xJf bubblewrap.tar.xz --strip-components=1 \
    && meson setup _build --prefix=/usr \
        -Dbash_completion=disabled \
        -Dman=disabled \
        -Dselinux=disabled \
        -Dsupport_setuid=false \
        -Dtests=false \
        -Dzsh_completion=disabled \
    && meson compile -C _build \
    && meson install -C _build

# --- runtime stage --------------------------------------------------------
# Debian (not distroless/alpine) because the optional facilities need a real
# base: bubblewrap for --isolation suite, chromium for the browser amenity.
# Both stay OPTIONAL at runtime — hostel probes them at boot and degrades
# honestly, so a locked-down pod without namespaces still serves. No --platform
# here: this stage IS the target platform, so apt installs the correct arch.
FROM debian:bookworm-slim

# WITH_CHROMIUM=false builds a lean image (~150MB) with no browser amenity;
# use the attach mode (--chromium-cdp-url) or a sidecar in that case.
ARG WITH_CHROMIUM=true

RUN apt-get update && apt-get install -y --no-install-recommends \
        bash ca-certificates libcap2 tini \
    && if [ "$WITH_CHROMIUM" = "true" ]; then \
        apt-get install -y --no-install-recommends chromium fonts-liberation; \
    fi \
    && rm -rf /var/lib/apt/lists/*

COPY --from=bwrap-builder /usr/bin/bwrap /usr/bin/bwrap
COPY --from=bwrap-builder /src/bubblewrap/LICENSE /usr/share/doc/bubblewrap/copyright
RUN bwrap --version

COPY --from=pathshim-builder /tmp/pathshim /usr/bin/pathshim
COPY --from=pathshim-builder /src/pathshim/LICENSE /usr/share/doc/pathshim/copyright
RUN pathshim --version

# /usr/local is the carrier-wide mutable software environment exposed to beds.
# Keep the daemon itself in the read-only system image so a package install
# cannot replace the binary used by future confiner re-execs or restarts.
COPY --from=builder /out/hostel /usr/bin/hostel

# Runs as root by default (like execd): bwrap must mkdir the /workspace mount
# point on the read-only root and needs user namespaces, and chromium runs with
# --no-sandbox — all awkward as non-root. Harden per deployment by running with
# a dropped-capability securityContext, or build a non-root variant once your
# pod grants bwrap the specific caps it needs.

# Carrier software uses ecosystem-standard env names; Hostel selects these for
# beds through its default allowlist instead of passing the whole daemon env.
ENV PYTHONUSERBASE=/usr/local \
    NPM_CONFIG_PREFIX=/usr/local \
    UV_TOOL_DIR=/usr/local/share/uv/tools \
    UV_TOOL_BIN_DIR=/usr/local/bin \
    PATH=/usr/local/bin:${PATH} \
    HOSTEL_WORKSPACE_ROOT=/workspace \
    HOSTEL_ISOLATION=suite \
    HOSTEL_PATHSHIM=/usr/bin/pathshim \
    HOSTEL_CHROMIUM_PATH=/usr/bin/chromium
# No VOLUME: it would bake the default path in at build time and silently
# stop applying when HOSTEL_WORKSPACE_ROOT is overridden. To keep beds across
# container restarts without an object store, mount a volume at the workspace
# root yourself (-v beds:/workspace); with --store s3 the local workspace is
# just a disposable working copy.
EXPOSE 8872

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD ["hostel", "--health"]

# tini as PID 1: hostel spawns shells and (via the amenity) chromium, both of
# which fork children — a real init reaps zombies and forwards signals.
ENTRYPOINT ["/usr/bin/tini", "--", "hostel"]
