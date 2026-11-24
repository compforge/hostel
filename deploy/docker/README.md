# Docker image

The repository Dockerfile builds the production Hostel image for `linux/amd64`
and `linux/arm64`. Run `make image` from the repository root for the current
architecture, or `make image-multiarch IMAGE=<repository>:<tag>` to build and
push both architectures.

The runtime image sets `HOSTEL_PUBLIC_GID` to `199999` by default. To reserve a
different supplementary group for Beds, build with `make image HOSTEL_PUBLIC_GID=198888`.
The lean and multi-architecture targets accept the same setting. The image grants
that group write access to uv's tool environments in `/usr/local/share/uv/tools`
and its command links in `/usr/local/share/uv/bin`. Images that provide uv can
reuse these paths. Add the command directory to the consuming Pillow's `PATH`
to run the installed tools by name.

A derived image can grant the group access to another directory using the inherited value:

```dockerfile
FROM hostel:dev
RUN install -d -m 2770 -g "${HOSTEL_PUBLIC_GID}" /srv/shared-work
```

Mount that directory into the intended Bed view with write access. The setgid bit
lets new entries inherit the directory's group. See the [public resource group contract](../../docs/isolation.md#hostel_public_gid-公共资源组).

## Process path helpers

The image installs pinned PRoot and pathshim binaries in `/usr/bin`, which is
already part of `PATH`, together with PRoot's `libtalloc2` runtime dependency.
Hostel discovers the conventional command names `proot` and `pathshim`; it has
no per-helper path environment variable or flag. A custom image can omit either
candidate, or provide an executable with the same command name anywhere in
`PATH`. `/v1/status` records discovery and smoke-probe facts separately.

PRoot supplies a best-effort per-Bed process view only when ptrace and its own
smoke probe work. It does not raise the reported Hostel isolation level.

PRoot is GPL-2.0 software. The image installs its license, Hostel modification
notice, and complete corresponding modified source under
`/usr/share/doc/proot/`; derived images that redistribute PRoot must preserve
those materials. Kubernetes permission and verification steps are documented
in [`../k8s/README.md`](../k8s/README.md#enable-ptrace-for-the-proot-workspace-view).
