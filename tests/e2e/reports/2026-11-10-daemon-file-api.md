# Daemon BedFS file API

Executed on 2026-11-10 from `worktree-daemon-bedfs-api`, based on main
`05ab2bee` plus the uncommitted implementation and tests. Runtime version: 0.2.20.
The daemon and E2E runner were built from this same tree. This is binary validation,
not a complete runtime image release or deployment.

## Contract and coverage

Handlers retain their existing Bed operation and call the prepared default-Pillow
BedFS directly. No credential worker is started. Files are scoped to Bed Rootfs and
explicit mounts; implicit Carrier dependencies are process-only. Explicitly enabled
exclusive-Carrier read fallback remains a documented compatibility exception.
Mount read-only policy still applies. Unix authority is the daemon's: declared
HostPath data may be accessible through the API when Bed processes cannot access it.
Managed files retain Bed ownership; borrowed sources are not automatically chowned.

Added cases:

- `TestDaemonFileAPIScope`: two Beds, native and both Execd entrypoints, independent
  data, upload ownership usable by Bed processes, rejection of Carrier/neighbor
  reads and escaping symlink reads/writes, nonblocking FIFO rejection, and private
  data under a system-looking `/usr/local` path.
- `TestFileAPIUploadHoldsBed`: a live streaming upload prevents eviction; completing
  the request releases the operation, eviction succeeds, and later reads do not
  recreate the missing Bed.

Updated regression expectations distinguish daemon HostPath access from process
DAC, and keep read-only volume / Pillow-consumer coverage.

## Execution and results

| Run | Conditions | Result |
| --- | --- | --- |
| `repocli validate --repo . --check format,lint,test --timeout 10m` | Full root and protocol components; canonical Makefile checks and race tests | Passed |
| `make build linux` | Native macOS arm64, Linux amd64 and arm64 | Passed; Linux arm64 build only |
| Local file API E2E | macOS arm64, kernel 27.2.0; real colocated daemon | 5 leaf cases passed, no skips |
| Linux file API E2E | Debian 12 amd64, kernel 5.15.120.bsk.3-amd64; disposable privileged container | 14 leaf cases passed, no skips |
| Linux BedFS package tests | Same container, root; cross-compiled Go test runner | Passed, no skips, including ownership and confinement |

Linux automatic filesystem cases realized bwrap/mount private roots and read-write /
read-only mappings; explicit mechanism-off controls realized the Carrier view.
Both local and supervisor backends ran. API assertions verify response content,
command completion, ownership, source retention and lifecycle behavior, rather than
only HTTP readiness. The standard target fixture starts real daemons and cleans
up processes and temporary Beds.

The base image was the already-cached `hub.byted.org/infcprelease/bedbox:base.5`,
image config `sha256:0215bd6baa1f3eb59743eb67e839c516cc1d2455c4b7057f01e0b19030091bf4`.
The runner and daemon were mounted into this image; it was not rebuilt. Privileged
container observations report seccomp 0. Host/runner/target facts in the native
verdict are collected inside the colocated container. No host sysctl changed.

Local command:

```sh
HOSTEL_E2E_ROOT=/tmp/hostel-daemon-bedfs-e2e make e2e \
  E2E_ARGS='-run Test\(DaemonFileAPI\|FileAPIUpload\|FilesystemAPIContract\|FilesystemPermissions\) -timeout 5m'
```

Linux compiled runner selection:

```sh
/artifacts/bedfs-linux.test -test.v -test.timeout 2m
/artifacts/e2e-linux.test -test.v \
  -test.run 'Test(DaemonFileAPI|FileAPIUpload|FilesystemAPIContract|FilesystemPermissions|HostPathRequiresSourcePermission|BedVolumeConsumers)' \
  -test.timeout 5m
```

The container uses `--privileged`, `--rm`, its artifacts directory mounted at
`/artifacts`, `HOSTEL_E2E_BINARY=/artifacts/hostel-e2e-linux`,
`HOSTEL_E2E_ENVIRONMENT_FILE=/artifacts/config.yaml` (the repository devbox config),
`HOSTEL_E2E_ON_TARGET=1`, `HOSTEL_E2E_SSH_HOST=devbox`,
`HOSTEL_E2E_REVISION=05ab2bee-daemon-bedfs-dirty`, and
`HOSTEL_E2E_RUNS_DIR=/artifacts/runs`. Connection: GSSAPI SSH to the configured amd
Devbox, followed by the existing Docker runtime. Test containers were removed;
artifacts remain in the run-owned remote `daemon-bedfs-api/e2e-artifacts` directory.

## Evidence and limits

Native verdicts under ignored `runs/hostel-runtime/`:

- `20261110T122049.577131000Z`: final macOS pass.
- `20261110T122144.875881785Z`: final Linux pass.

Logs, the source snapshot and cleanup observation are retained in ignored
`runs/daemon-file-api-20261110/`. The source patch includes the new E2E file and
excludes this report. Artifact SHA-256:

| Artifact | SHA-256 |
| --- | --- |
| `bin/hostel-e2e` | `2b9415da05a417bdf219cd14ba9809575dc6ac9a3aaca9b52e654fd6a47b3f51` |
| `bin/hostel-e2e-linux` | `372a6fffc0c1f7985e828476b00c74494b9e185acbe704c48994470be299d263` |
| `bin/e2e-linux.test` | `257b80c7296d23beabe07e26e35c44f03eed59bf83dd0cfe6b1f41acf9a37779` |
| `bin/bedfs-linux.test` | `c76745863e9665a3a447dfafbd0486a7e036d8544e30ede0580930b8de3e6060` |
| `runs/daemon-file-api-20261110/source.patch` | `f650803c5a165c33d3298ab664973dee4ae75488bc10d717eae65763252d03e6` |

Earlier validation exposed stale common-directory fixtures: they were converted
to explicit mounts, with a canonical source-path assertion for macOS symlinks.
Final full validation passed after those fixes. An initially expired SSH ticket
was refreshed through the existing credential workflow before Linux execution.
Container-created evidence initially had root-only permissions; ownership of only
this run's artifact directory was returned to the test user before collection.

An additional `TestSharedSoftwareProvidedPermissions` attempt did not reach the
suite: the base image lacks `setfacl`, so explicit shared-directory fixture setup
failed with exit 127. That changed scenario remains unverified in this environment;
its new API-private-path behavior is independently covered by the passing scope
case. No dependency was installed and the failed setup container was removed.

This selection does not establish full userland/package-manager behavior, older
kernels, arm64 runtime namespaces, rootless Linux, PRoot/pathshim live behavior,
Kubernetes orchestration or the sandctl business chain.
