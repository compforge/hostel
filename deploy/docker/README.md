# Docker image

The repository Dockerfile builds the production Hostel image for `linux/amd64`
and `linux/arm64`. Run `make image` from the repository root for the current
architecture, or `make image-multiarch IMAGE=<repository>:<tag>` to build and
push both architectures.

## PRoot runtime dependency

The image installs the pinned Termux PRoot version declared in
[`Dockerfile`](Dockerfile), together with `libtalloc2`. Hostel uses it only as a
best-effort per-Bed `/workspace` view when the runtime permits ptrace and a
stronger mount view is unavailable. PRoot does not raise the reported Hostel
isolation level.

The image also sets `HOSTEL_PROOT=/usr/bin/proot`. A deployment may disable the
candidate by passing `--proot ""` to Hostel. A custom runtime image that keeps
PRoot support must provide the binary, its dynamic library, and either the same
environment setting or an explicit `--proot` path.

PRoot is GPL-2.0 software. The image installs its license, Hostel modification
notice, and complete corresponding modified source under
`/usr/share/doc/proot/`; derived images that redistribute PRoot must preserve
those materials. Kubernetes permission and verification steps are documented
in [`../k8s/README.md`](../k8s/README.md#enable-ptrace-for-the-proot-workspace-view).
