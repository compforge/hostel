# Docker image

The repository Dockerfile builds the production Hostel image for `linux/amd64`
and `linux/arm64`. Run `make image` from the repository root for the current
architecture, or `make image-multiarch IMAGE=<repository>:<tag>` to build and
push both architectures.

## Workspace helpers

The image installs pinned PRoot and pathshim binaries in `/usr/bin`, which is
already part of `PATH`, together with PRoot's `libtalloc2` runtime dependency.
Hostel discovers the conventional command names `proot` and `pathshim`; it has
no per-helper path environment variable or flag. A custom image can omit either
candidate, or provide an executable with the same command name anywhere in
`PATH`. `/v1/diagnostics` records discovery and smoke-probe facts separately.

PRoot supplies a best-effort per-Bed process view only when ptrace and its own
smoke probe work. It does not raise the reported Hostel isolation level.

PRoot is GPL-2.0 software. The image installs its license, Hostel modification
notice, and complete corresponding modified source under
`/usr/share/doc/proot/`; derived images that redistribute PRoot must preserve
those materials. Kubernetes permission and verification steps are documented
in [`../k8s/README.md`](../k8s/README.md#enable-ptrace-for-the-proot-workspace-view).
