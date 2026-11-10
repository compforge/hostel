# Bubblewrap upgrade and Bed filesystem view

Executed on amd devbox on 2026-11-10, using the existing compiled E2E suite and
Hostel main `9e6c199e` plus the uncommitted `fs-view-boundary` changes. These remove
recursive public-directory permission preparation and upgrade bubblewrap.

The tested base was built from Bedbox's `bwrap0131-master` worktree, with
bubblewrap 0.13.1. Its amd64 image config is
`sha256:0215bd6baa1f3eb59743eb67e839c516cc1d2455c4b7057f01e0b19030091bf4`.
Hostel and the runner were mounted into disposable containers; this was not a
complete Bedbox runtime or customer deployment.

The same amd64 image config was subsequently published in
`hub.byted.org/infcprelease/bedbox:base.5`, whose dual-architecture index is
`sha256:ab85838e9f71fe9316216928f57e20e9460f8fec41cf10d9eb55d00fc58977b2`.
Both architecture builds executed the bubblewrap version check; the runtime
namespace tests below ran only on amd64.

## Results

Kernel: `5.15.120.bsk.3-amd64`; Debian 12 userland; container-local
`net.ipv4.ip_forward=0`. No host sysctl was changed.

Selection: `^(TestRootfulPreparedMount|TestFilesystemPermissions)$`.
Both tests exercise local and supervisor execution; the permissions test also
runs an explicit Carrier-view control, for six leaf cases in total.

| Container conditions | Result |
| --- | --- |
| Privileged, seccomp disabled | All six cases passed |
| Default Docker seccomp plus SYS_ADMIN/SYS_PTRACE | Four permissions cases passed with UID/Carrier fallback; both required-bwrap cases failed at pivot_root |
| SYS_ADMIN/SYS_PTRACE plus seccomp=unconfined, not privileged | All six cases passed |

The passing mount runs verify private roots, read-write/read-only volume access,
file API and command/session agreement, external source retention after purge,
and prepared Pillow views. Bed workloads retain a nonzero UID, zero capabilities
and no_new_privs. Public sources in these fixtures are explicitly provisioned;
these results do not establish arbitrary system-directory or cross-UID package
replacement permissions.

The restricted run reported `bwrap: pivot_root: Operation not permitted`.
A direct `bwrap --bind / / /bin/true` control failed identically with the existing
0.11.2 image and the new 0.13.1 image. Disabling only seccomp made both succeed;
disabling only AppArmor did not. This is an existing runner-policy restriction,
not evidence of a new bubblewrap regression. No seccomp policy was changed in
product code. Auto degrades; required fails explicitly.

## Evidence

Native verdict IDs under `runs/hostel-runtime/`:

- `20261110T074008.013648825Z`: privileged pass.
- `20261110T074031.824031312Z`: default-seccomp diagnostic failure.
- `20261110T074134.190496595Z`: capabilities plus unconfined-seccomp pass.

The runner uses `tests/e2e/environments/devbox/config.yaml` with
`HOSTEL_E2E_ON_TARGET=1`; recorded host observations come from inside the colocated
container. Logs and verdicts are retained in `/tmp/hostel-base5-evidence` locally
and the run-owned `base5-e2e` directory in the mirrored devbox worktree.

| Artifact | SHA-256 |
| --- | --- |
| hostel-e2e | 31bc370cccfabbbe27b8fa7f4532eea6a3b5a5168a285af53ab9b86f92ffa6ba |
| e2e.test | 734e77e90470ccba90418688fb305e3292d3ab1236396581eee5737431f4bade |
| source.patch | edcd965b59140c5fda03c6a96ee9587949aa74ef94efb6c606c7797a552b5e83 |

This selection does not cover older kernels, arm64 runtime namespaces, PRoot,
pathshim, egress traffic, or the full userland suite.
