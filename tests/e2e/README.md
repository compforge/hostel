# Single-machine E2E

This suite owns Hostel's executable runtime contract. It starts a real Hostel
binary or container on the runner, talks only to the public HTTP API, and cleans
every process, container, bed, and temporary workspace it creates. Kubernetes,
sandctl placement, and cross-carrier orchestration are intentionally outside
this boundary.

## Run the core contract

```sh
make e2e
```

The core profile also covers remote MCP configuration, list/call, metadata,
call-scoped overrides and cross-bed isolation over SSE and Streamable HTTP.

The core profile covers health and capability reporting, asynchronous bed
creation, inventory invariants, capacity, foreground/background execution,
status/log/interrupt control, stateful and isolated sessions, the complete
file/directory mutation round trip, file/command interoperability, cross-bed
access, active-bed eviction safety, noop eviction starting fresh, purge, and the
dorm/room/suite isolation resolution available on the host.

The binary profile also verifies per-Bed Store precedence against a runner-local
S3 endpoint: API noop bypasses configured S3 throughout the Bed lifecycle, and
API S3 overrides a noop instance default. The endpoint deliberately denies S3
requests; this case proves backend selection and error propagation, not remote
backup integrity. The opt-in successful round trip below covers durable formats
against a real S3-compatible service.

By default, a host may honestly degrade an unavailable isolation request. A
release runner can require levels to be realized instead:

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

## Run successful mixed-Store persistence

`TestMixedBedStoresRoundTrip` requires binary mode and a disposable S3-compatible
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
  GOFLAGS='-run=TestMixedBedStoresRoundTrip' make e2e
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
| `HOSTEL_E2E_PROOT` | PRoot binary used to verify the ptrace-based `/workspace` fallback. |

The binary and image variables are mutually exclusive. Image mode is intended
for a Linux runner with Docker because the target container uses host networking
to reach the test-owned browser fixture.

## Network namespace contract

In a disposable Linux container with the required namespace, routing and nft
permissions, set `HOSTEL_E2E_REQUIRE_NETWORK=1` and run `make e2e` in binary
mode. `TestNetworkNamespaces` requires an enabled diagnostic verdict and checks
the `local` / `supervisor` × `dorm` / `room` / `suite` matrix: separate Bed namespaces,
command/session namespace consistency, zero capability sets, `no_new_privs`, and
purge/recreate. Set `HOSTEL_E2E_REQUIRE_ISOLATION=dorm,room,suite` to reject file-level
degradation as well. Binary fixtures use a traversable workspace root so a selected
Bed UID can access its own absolute paths.
The runtime prerequisite is a complete successful probe, not just `NET_ADMIN`.
Do not run this privileged profile with the image fixture's host networking.

`TestNetworkPolicyTraffic` uses the same network profile and both Executor
backends. A test-owned TCP listener runs in the carrier, and a small subprocess
of the compiled E2E runner connects from inside each Bed through the public
command/session APIs. Keep the runner executable at a path readable inside the
Bed (for example `/hostel-test/e2e.test` when mounting compiled binaries into a
container). No curl, Python, external website or public Internet is required.

The case verifies initial deny before Ready, explicit IP/CIDR allows, overlapping
deny precedence, PATCH/DELETE changes, POST/PUT replacement, rejected updates
preserving the old policy, live session observations, unaffected sibling Beds,
and policy reset after eviction/recreation. A control Bed proves the service is
reachable before denial is asserted; connection refusal and bad replies fail
rather than counting as policy rejection. Daemons, listeners, namespaces and
workspaces are run-owned and cleaned up by the fixture.

These traffic assertions cover new IPv4 TCP connections. DNS/domain rules,
TTL expiry, existing established flows and injected nft transaction failures are
not covered by this E2E case; component tests cover parts of those behaviors.
