# Single-machine E2E

This suite owns Hostel's executable runtime contract. It starts a real Hostel
binary or container on the runner, talks only to the public HTTP API, and cleans
every process, container, bed, and temporary workspace it creates. The same
binary suite can run on a host or inside a Kubernetes Pod. Environment selection,
observations and runner setup are described in [E2E environments](e2e-environments.md).
Control-plane placement and cross-carrier orchestration remain outside this boundary.

## Run the core contract

```sh
make e2e
```

The core profile verifies global Subsystem/Amenity reports, per-Bed subsystem
and bound Tenant reports, read-only status polling, and new Tenant identity
after recreating a same-name Bed. It also covers remote MCP configuration, list/call, metadata,
call-scoped overrides and cross-bed isolation over SSE and Streamable HTTP.

`TestExplicitBedIdleRelease` verifies empty startup, required Bed selectors, Bed capacity and idle reclamation, and return to `releasable` after expiry or purge.

The core profile covers health and capability reporting, asynchronous bed
creation, inventory invariants, capacity, foreground/background execution,
status/log/interrupt control, stateful and isolated sessions, the complete
file/directory mutation round trip, file/command interoperability, cross-bed
access, active-bed eviction safety, noop eviction starting fresh, purge, and the
dorm/room/suite isolation resolution available on the host.

The binary profile also verifies per-Volume persistence policy against a runner-local
S3 endpoint: persistence=none bypasses configured S3 throughout the Bed lifecycle, and
explicit persistence=sync requires a durable backend even with a noop instance default. The endpoint deliberately denies S3
requests; this case proves backend selection and error propagation, not remote
backup integrity. The opt-in successful round trip below covers durable formats
against a real S3-compatible service.

Hostel reports the isolation guarantees actually available on the host. A
release runner can assert the attained room as a caller-side expectation (both filesystem and network must reach the expected grade):

```sh
HOSTEL_E2E_REQUIRE_ISOLATION=dorm,room,suite make e2e
```

To exercise the best-effort dorm/room `/workspace` process view, provide a
Linux pathshim v0.1.6 binary (the revision pinned by the image). The isolation suite then requires canonical cwd, file
API interoperability, mapped executable startup, session descendants, and
terminal signal behavior through pathshim:

```sh
HOSTEL_E2E_PATHSHIM=/usr/local/bin/pathshim make e2e
```

To verify the ptrace-based fallback when pathshim is unavailable, provide a
Linux PRoot binary. Image mode uses the PRoot bundled in the image:

```sh
HOSTEL_E2E_PROOT=/usr/local/bin/proot make e2e
```

With `HOSTEL_E2E_PATHSHIM` in binary mode, the concurrent dorm unzip regression
explicitly disables PRoot in the disposable daemon PATH and requires `mode=pathshim`.
A successful higher-priority PRoot selection cannot count as pathshim verification.
v0.1.3 does not implement the `probe --bind ...` subcommand and is unsupported.

These variables are test-harness inputs: the launcher prepends their parent
directories to the target `PATH`. Hostel itself has no per-helper path setting.

## Compare filesystem behavior under permissions

`TestFilesystemPermissions` checks two Beds' root data, read-write/read-only
HostPath volumes/mounts, file API fallback, command/session interoperability, and external
source retention after purge. It runs with automatically selected filesystem
features and a separate carrier-view control. Use the same test selector across
the Kubernetes permission profiles in [E2E environments](e2e-environments.md#rootfs-与-hostpath-volumesmounts-权限矩阵).

```sh
make e2e E2E_ARGS='-run TestFilesystemPermissions -timeout 3m'
```

## Verify Bed port mappings

`TestBedPortMappings` starts two HTTP Pillows using dynamic ports on shared networking and the same fixed containerPort in separate private networks.
For local and supervisor executors it checks per-Bed status and access agreement,
external HTTP access, Bed-command access through loopback and the discovered `bed_port`, restart
identity, distinct Host ports, and allocation cleanup on eviction. It requires the
binary profile and Python 3. The supervisor executor requires Linux. Shared networking with
the local executor runs on macOS/Linux; private netns
is opt-in and requires the disposable Linux network profile. Logical-scope unit
tests do not substitute for that profile.

```sh
make e2e E2E_ARGS='-run TestBedPortMappings -timeout 3m'
HOSTEL_E2E_REQUIRE_NETWORK=1 make e2e E2E_ARGS='-run TestBedPortMappings -timeout 3m'
```

## Run native Bed root and Pillow main process cooperation

`TestPillowExecutableUsesBedView` covers mount/PRoot × local/supervisor.
It starts Pillows by absolute path, relative path and a bare name found only
through the Pillow main process's PATH, then verifies their files through a Bed command.
Tools are required for each selected mechanism; unavailable tools are reported
as skips, not isolation proof.

The rootful regression uses a private Rootfs and a managed `0700` workspace owned by the non-root Bed identity;
command, Session and Pillow main processes must enter their prepared views with zero
capabilities and `no_new_privs`. It must not pass by falling back to UID 0.

To require rootful preparation, use a Linux host with the namespace and identity
management capabilities, plus bwrap. `make e2e` builds the static Hostel helper:

```sh
HOSTEL_E2E_REQUIRE_ROOTFUL=1 make e2e E2E_ARGS='-run ^TestRootfulPreparedMount -timeout 2m'
```

This checks that separate commands, a session and a Pillow main process share one prepared
mount namespace, run with non-root Bed identity, empty capability sets and
`NoNewPrivs=1`, and receive the workload environment. Running it with the host's
unprivileged user namespace switch disabled proves the rootful path without
altering that switch. Missing required support fails rather than skips.

`TestBedRootPillowCommandFiles` uses two live Pillow main processes with identical
absolute paths under `/mnt`, `/tmp`, and a new root-level directory. The
`file-api` and `executor` subcases each run three rounds with distinct per-Bed
inputs. They verify Executor/file API writes → Pillow main process reads and writes →
Executor/file API reads. Inputs and outputs are rechecked after both Beds have
written, detecting cross-Bed overwrites and reads of another Bed's input.
It covers local/supervisor executors with bwrap and PRoot,
requires the selected root view without degradation, and purges both Beds.
On non-Linux it skips; an absent helper skips unless explicitly supplied. Run on
a disposable Linux runner with the required helpers:

```sh
make e2e E2E_ARGS='-run ^TestBedRootPillowCommandFiles -timeout 3m'
```

This is a native filesystem contract, not a proof of pathshim root redirection,
complete container isolation, or a business-specific service integration.

## Run successful mixed-Store persistence

`TestManagedVolumeRoundTrip` requires binary mode and a disposable S3-compatible
endpoint (path-style addressing, `us-east-1`, static credentials). The fixture
creates a uniquely named bucket, verifies connectivity through the S3 SDK, and
removes that bucket and its objects during cleanup, including after a failure.
Credentials must allow bucket creation/deletion and object read/write/list/delete.
It never empties a pre-existing bucket.

For example, start a temporary MinIO service on the same runner:

```sh
docker run -d --rm --name hostel-e2e-s3 -p 127.0.0.1:19000:9000 --tmpfs /data \
  -e MINIO_ROOT_USER=hostel-e2e -e MINIO_ROOT_PASSWORD=hostel-e2e-test-only \
  quay.io/minio/minio:RELEASE.2025-04-22T22-12-26Z server /data
# Wait for /minio/health/ready to return 200 before starting the suite.
HOSTEL_E2E_REQUIRE_S3=1 HOSTEL_S3_ENDPOINT=http://127.0.0.1:19000 \
  HOSTEL_S3_ACCESS_KEY_ID=hostel-e2e HOSTEL_S3_SECRET_ACCESS_KEY=hostel-e2e-test-only \
  GOFLAGS='-run=TestManagedVolumeRoundTrip' make e2e
docker stop hostel-e2e-s3
```

The test runs cas, pack, tar and noop Beds together, with two distinct cas Beds.
It checkpoints different binary payloads at identical paths, evicts every Bed,
verifies the local copies are removed, and restores using a second daemon with a
fresh workspace root and a different default Store. Durable payloads must match
byte-for-byte; noop must return an empty workspace. Purging one Bed must not
remove a sibling's remote snapshot, and purged data must not reappear. All remote
objects must be gone after the final purge.

Without `HOSTEL_E2E_REQUIRE_S3=1` this case skips. Once requested, missing
configuration, an unavailable endpoint or a failed bucket setup fails the run;
it is not reported as a successful persistence check. This covers the real S3
protocol and Hostel lifecycle, not provider-specific IAM, multipart limits or
cross-region consistency.

## Run the image/userland contract

```sh
make e2e-image E2E_IMAGE=registry.example/hostel-or-bedbox:tag
```

Image mode uses Docker host networking and additionally requires:

- a PyPI package installed into `/usr/local` remains importable in a later
  execution;
- a global npm package installed into `/usr/local` remains loadable in a later
  execution;
- Chromium can navigate, type, click, wait, read page text, capture a
  screenshot into the bed workspace, and expose that PNG through the file API.

These are required cases in image mode. A missing package manager, unavailable
network, absent browser amenity, or unreadable artifact fails the run rather
than becoming a skip.

## Environment contract

| Variable | Meaning |
| --- | --- |
| `HOSTEL_E2E_BINARY` | Real Hostel binary started by the test fixture. Set by `make e2e`. |
| `HOSTEL_E2E_IMAGE` | Container image started by the fixture. Set by `make e2e-image`. |
| `HOSTEL_E2E_USERLAND=1` | Enables required PyPI/npm/Chromium cases; image target sets it. |
| `HOSTEL_E2E_REQUIRE_ISOLATION` | Comma-separated requested levels that must not degrade. |
| `HOSTEL_E2E_PATHSHIM` | pathshim binary used to verify the dorm/room `/workspace` process view. |
| `HOSTEL_E2E_REQUIRE_S3=1` | Require the successful mixed-Store round trip against the configured S3 endpoint; the fixture owns a fresh bucket. |
| `HOSTEL_E2E_REQUIRE_NETWORK=1` | Require real netns and IP/CIDR policy traffic checks in a disposable Linux binary runner. |
| `HOSTEL_E2E_REQUIRE_ROOTFUL=1` | Require prepared rootful mount/identity checks; combine with NETWORK for the resolver regression. |
| `HOSTEL_E2E_PROOT` | PRoot binary used to verify the ptrace-based `/workspace` fallback. |

The binary and image variables are mutually exclusive. Image mode is intended
for a Linux runner with Docker because the target container uses host networking
to reach the test-owned browser fixture.

## Network namespace contract

In a disposable Linux container with the required namespace, routing and nft
permissions, set `HOSTEL_E2E_REQUIRE_NETWORK=1` and run `make e2e` in binary
mode. `TestNetworkNamespaces` requires a private network diagnostic verdict and checks
the `local` / `supervisor` matrix: separate Bed namespaces with best-effort capability selection,
command/session namespace consistency, zero capability sets, `no_new_privs`, and
purge/recreate. Set `HOSTEL_E2E_REQUIRE_ISOLATION=dorm,room,suite` to reject profile
degradation as well. Binary fixtures use a traversable workspace root so a selected
Bed UID can access its own absolute paths.
The runtime prerequisite is a complete successful probe, not just `NET_ADMIN`.
Do not run this privileged profile with the image fixture's host networking.

`TestNetworkPolicyTraffic` uses the same network profile and both Executor
backends. A test-owned TCP listener runs in the carrier, and a small subprocess
of the compiled E2E runner connects from inside each Bed through the public
command/session APIs. Keep the runner executable at a path readable inside the
Bed (for example `/usr/local/lib/hostel-e2e/e2e.test` when mounting compiled
binaries into a container). The private root view inherits runtime directories
such as `/usr`; an arbitrary carrier path such as `/hostel-test` is not visible
inside the Bed without an explicit volume mount. No curl, Python, external website or public Internet is required.

The case verifies initial deny before Ready, explicit IP/CIDR allows, overlapping
deny precedence, PATCH/DELETE changes, POST/PUT replacement, rejected updates
preserving the old policy, live session observations, unaffected sibling Beds,
and policy reset after eviction/recreation. A control Bed proves the service is
reachable before denial is asserted; connection refusal and bad replies fail
rather than counting as policy rejection. Daemons, listeners, namespaces and
workspaces are run-owned and cleaned up by the fixture.

These traffic assertions cover new IPv4 TCP connections. DNS/domain rules,
TTL expiry, existing established flows and injected nft transaction failures are
not covered by this E2E case; subsystem tests cover parts of those behaviors.

`TestNetworkNAT` uses the same disposable Linux binary profile. It creates an
external fixture namespace on `192.0.2.0/30` after rejecting overlapping carrier
routes. The fixture has only its connected route, so Bed replies require
masquerade. For both executors and two Beds, the service must observe the
carrier-side source address. No public network or DNS is needed. Fixture
processes, veth and namespace are removed after the run. Run this case on actual
Linux 4.19 and a newer kernel to verify the IPv4 NAT compatibility boundary;
a container image alone does not change the host kernel.

```sh
HOSTEL_E2E_REQUIRE_NETWORK=1 make e2e E2E_ARGS='-run ^TestNetworkNAT$ -timeout 2m'
```

`TestRootfulNetworkResolver` requires both rootful and network profiles. It checks
that command and Pillow main process see the same Bed gateway resolver and can resolve
`example.com`. Unlike the local policy traffic case, this requires working
upstream DNS and `getent` in the runner. Missing prerequisites fail the requested
profile; the test never installs tools or changes host forwarding settings.

## Internal subsystem configuration

`make e2e` builds `bin/hostel-e2e` with the `e2e` build tag. The fixture can pass
`config.Options` through `targetOptions.config`; pointer presence distinguishes
explicit Auto/false/zero from omission. The test-only `HOSTEL_E2E_CONFIG` file is
private to the run and is removed with the fixture. Production binaries/images
have no internal configuration input; configuration-specific cases run in binary mode.

```bash
make e2e E2E_ARGS='-run ^TestFeature'
```

`TestFeaturePoliciesOff` disables filesystem helpers/boundaries, NetNS and cgroup,
asserts the public status reports disabled/not-probed, executes a Bed command,
and verifies the network policy endpoint rejects unavailable enforcement.
`TestFeatureRequiredStartupFailure` requires an absent helper or supplies conflicting
room/feature settings and asserts prompt daemon failure with the specific cause.
These cases do not skip on missing optional host features. Existing config unit tests
cover explicit S3 and zero-valued settings overriding environment defaults.

Configuration restrictions exercise selection; they do not prove kernel permission
rejection or failure halfway through allocation. Keep real restricted-container
cases alongside these deterministic tests. See [configuration design](../../docs/isolation.md).

Status schema 13 reports subsystem tool selection under `tools`, groups boot-time host facts under `host.fact` and managed port
allocations under `host.status.ports`. It separates `healthz.isolation` (effective room summary) from
`/v1/status.subsystems.filesystem` (shared/confined/private). Filesystem assertions
must use the latter: a degraded room profile may retain a private file view.
These cases require an explicit E2E run; compile-only validation is not execution evidence.

## Bed temporary directories

`TestBedTemporaryDirectory` verifies command output capture, session reads of API uploads,
Pillow main process cooperation, Execd downloads, API deletion observed by a later command, and identical
file names in two Beds. It runs carrier/pathshim/PRoot/mount with local and Linux supervisor
executors, requires each selected mechanism, and purges its Beds. Carrier uses the generated
`TMPDIR`; the other modes additionally require literal `/tmp`. Explicit helper inputs require
that helper; unavailable prerequisites are reported as skips.

```sh
make e2e E2E_ARGS='-run ^TestBedTemporaryDirectory -timeout 5m'
```

When testing a custom binary with `/tmp` redirection, install or mount it outside `/tmp`
(for example `/usr/local/bin`): Hostel re-executes itself as `bedinit` inside the selected view.

`TestOpenSandboxAdapterLifecycle` covers both Execd addresses, directory creation, line downloads,
persistent session state, preparation-failure classification without losing the session, cross-Bed session rejection, background partial output after the launch
response closes, byte-cursor polling, cancellation and explicit unsupported code kernels.
Run with `make e2e E2E_ARGS='-run TestOpenSandbox -timeout 3m'`. File Range, metadata, permissions,
large log retention and cleanup also have protocol/domain regression tests in the regular suite.

## Verify private process namespaces

On a Linux runner permitted to create PID/mount namespaces and mount procfs:

```sh
HOSTEL_E2E_REQUIRE_PID_NAMESPACE=1 make e2e E2E_ARGS='-run TestPrivatePIDNamespace -timeout 3m'
```

The cases require the supervisor, bwrap and Python 3. They check two distinct
Bed PID namespaces, matching procfs, one retained mount namespace shared by
commands/sessions/Pillows, real control-path bind mounts, and HTTP Pillow main process
listener ownership. The recovery case observes its own API-issued Executor ID,
kills only that supervisor, verifies the old process tree is dead, then checks
replacement namespace identity, retained Bed data and reuse of the new view. Missing prerequisites fail
this explicit run. No upper-layer migration protocol or application-specific
path is built into Hostel.

### 文件房型与增强能力

`TestPrivateFilesWithSharedNetwork` 在 rootful binary profile 下验证私有文件视图配合共享身份、
网络和 PID namespace 仍为 suite；通过 run-owned nft 失败验证真实网络探测降级，检查命令、
session、Pillow main process 与 file API 的一致性、跨 Bed 文件拒绝访问，以及显式 NetworkPolicy 不被忽略。
该用例与 `TestPrivatePIDNamespaceRecovery` 共同覆盖静态组合和 Executor 替换后的房型保证。
`TestFeatureRequiredStartupFailure/missing-network` 验证缺少网络子系统时 required 拒绝启动。

### Shared volumes and consumer views

`TestBedVolumeConsumers` covers managed/host_path × bwrap × local/supervisor with two Beds. It verifies
Pillow main process writes, read-only Pillow main process/command/file API access, cross-Bed separation,
Pillow main process restart preserving volume data, and Bed purge through the existing cleanup.
Run on a disposable Linux runner with bwrap; unavailable prerequisites skip or fail,
never count as successful mount validation:

```sh
make e2e E2E_ARGS='-run ^TestBedVolumeConsumers -timeout 3m'
```

`TestManagedVolumeRoundTrip` covers CAS/Pack/Tar/Auto with two Beds and two persistent volumes each,
changed mount paths on a fresh Carrier, temporary-volume and Rootfs exclusion, and purge cleanup.

## Subsystem realization reports

`TestIsolationLevels` checks actual subsystem grades and their aggregate room after Bed preparation.
Caller-side runner expectations may assert a room, but Hostel itself has no room admission gate.
`TestFeaturePoliciesOff` verifies file/command usability with mechanisms disabled and reports policy enforcement gaps.
Mechanism-specific mount/PID tests require their own tools without requiring a separate network backend.
`TestPrivateFilesWithSharedNetwork` verifies private file boundaries while reporting an overall dorm room
and explicitly unenforced network rules.

## Carrier IPv4 forwarding unavailable

`HOSTEL_E2E_REQUIRE_FORWARDING_DISABLED=1` enables `TestCarrierForwardingDisabled`.
The runner must already have `ip_forward=0` and the `ip`/`nft` tools. This case never
writes the sysctl: it verifies `cause=carrier`, `probe.stage=forwarding`, shared
networking with an unenforced requested policy, an unchanged filesystem grade,
working commands for local/supervisor, and startup rejection for required netns.
Use a disposable Pod with its own network namespace; do not disable forwarding
on a shared node to prepare this case.


## 共享 netns 的 UID 出站策略

`HOSTEL_E2E_REQUIRE_UID_POLICY=1` 启用 `TestSharedUIDNetworkPolicyTraffic`，要求 disposable
Linux Carrier 内 `ip_forward=0`、nft 可用且能分配独立 UID；自动软件组同时验证共享组不会改变 UID 网络归属。
该用例通过公共 API 对 local/supervisor 执行后端验证初始 BedSpec、不同 UID 共享同一 netns、
规则合并/删除、session 更新、非法更新不破坏旧策略及删除重建；策略状态必须为 ok/dns+nft。
`TestCarrierForwardingDisabled` 保留显式固定 UID 的客户条件，验证 unenforced 与 required netns 拒绝。

```sh
HOSTEL_E2E_REQUIRE_UID_POLICY=1 HOSTEL_E2E_REQUIRE_FORWARDING_DISABLED=1 \
  make e2e E2E_ARGS='-run "^(TestSharedUIDNetworkPolicyTraffic|TestCarrierForwardingDisabled|TestFeaturePoliciesOff)$" -timeout 5m'
```

Host 网络包的 `HOSTEL_UID_POLICY_TEST=enabled` / `TestUIDPolicyTraffic` 在同样的独占
Linux 条件下验证真实 nft、UDP/TCP DNS 重定向、域名解析后的地址学习、跨 UID DNS
listener 访问拒绝、TCP/UDP 出站、IPv6 阻断、既有连接撤销和同 UID 重新分配。
未显式启用时跳过；普通单元测试成功不算已经验证内核执行。不得为准备测试关闭共享节点的 forwarding。

### Bed software group

`TestBedSoftwareGroup` validates distinct identity allocation, exact groups and capability removal
through command/session execution for local and supervisor backends. `TestPrivateFilesWithSharedNetwork`
checks the same group in a Pillow main process under the private file view.
`TestSharedSoftwareAcrossBedUsers` (HOSTEL_E2E_USERLAND=1) uses a disposable image whose software permissions are prepared at runtime with
the internal software GID 199999, setgid directories and default ACLs to exercise cross-UID pip/npm/uv install,
load, replacement and uninstall. It mutates carrier-wide packages; never run it on a shared carrier.

## Verify UID egress across process lifecycles

The disposable Linux UID policy profile also owns these cases:

- `TestBedUIDPillowEgress`: local/supervisor Pillow main processes use the Bed UID
  and exactly the internal software group; allow/deny apply at startup and after
  policy reversal plus an asynchronous Pillow restart.
- `TestBedIdentityCrashRecovery`: kill only the fixture-owned daemon, start a new
  daemon over its retained beds-root, and admit a newcomer before redeclaring the
  old Bed. Verify UID reservation, Bed identity, private file ownership/access,
  software group membership, egress updates, and eventual purge/recreation.

Use a disposable carrier netns with root, CAP_NET_ADMIN, nft and ip_forward=0;
leave the host firewall and host sysctl untouched. The crash case deliberately
leaves kernel resources until recovery or carrier removal. Always remove the
carrier after a failed run. The same compiled suite runs through the documented
Linux container runner; the source entrypoint is:

```sh
HOSTEL_E2E_REQUIRE_UID_POLICY=1 make e2e \
  E2E_ARGS="-run 'TestBedIdentityCrashRecovery|TestBedUIDPillowEgress' -timeout 3m"
```

The crash cases assert both policy recovery and complete cleanup of owned allocation
records. See [the post-171 baseline](reports/2026-11-09-identity-recovery.md) for
the original failure and [the recovery fix run](reports/2026-11-09-network-reconciliation.md)
for current validation.

`TestPrivateNetworkCrashRecovery` is the independent-netns counterpart. Run with
`HOSTEL_E2E_REQUIRE_NETWORK=1` inside a disposable privileged Linux carrier with
ip_forward=1. It asserts that the retained Bed ID/UID receive a new netns and
that deny-to-allow policy updates work after the daemon crash. It also asserts that no namespaces, Carrier nft tables or allocation records remain
after both daemon fixtures close, and preserves a separate unowned nft table.
Remove the disposable carrier after the run.

### Shared software runtime preparation

`TestSharedSoftwareRuntimePermissions` checks that final-image startup prepares `/usr/local`
before Bed execution, then exercises cross-UID writes and replacement with umask 022.
A runner already inside the disposable image may instead set `HOSTEL_E2E_BINARY`
and `HOSTEL_E2E_ON_TARGET=1`, plus the separate `HOSTEL_E2E_DISPOSABLE=1`
acknowledgement, with the normal environment-aware binary suite. The disposable
flag authorizes global package mutations; ordinary on-target runs do not.
Run it with `TestSharedSoftwareAcrossBedUsers` for real pip/npm/uv installation, replacement
and uninstall. Both require a disposable userland image with
the internal software group and runtime directory preparation, with no numeric identity
or shared-directory configuration. The permissions case also uploads through the file API
and modifies the result from the other Bed. Select these through the normal `make e2e` entrypoint
(`E2E_ARGS='-run TestSharedSoftware'`) or the image suite; retain final-image identity.
Do not run the package mutations against a shared workstation or production Carrier.
Build and export/load the image before testing: an in-build or tmpfs-only ACL test does
not verify that runtime startup repairs permissions missing from the exported layers.
