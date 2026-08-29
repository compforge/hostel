# hostel

English | [简体中文](README.zh-CN.md)

**hostel is an agent-native sandbox runtime.** It runs many isolated sandboxes
from a single process and exposes an HTTP API to create them, run commands and
shell sessions in them, and read/write their files — built for AI agents that
each need a scratch space to execute in. Each sandbox is called a **bed**. It
runs anywhere: your laptop, a VM, a CI job, or a container.

Its resource and file APIs use [OpenSandbox](https://github.com/alibaba/opensandbox)
execd as a design baseline. Command execution is hostel-native: every run has
a stable execution id and a structured terminal result that preserves exit,
signal and termination-cause semantics.

## Why

If you give each agent (or user, or task) its own full VM or container, it's slow
to start and holds real CPU/RAM even while doing nothing — and agent workloads
sit idle most of the time (the agent spends most of its wall-clock waiting on the
model, not running commands). That's wasteful when you want many of them at once.

hostel takes a lighter approach: pack many isolated **beds** into one process.
A bed is near-instant to create and costs almost nothing while idle, so a single
machine or container can hold a large number of them. Isolation is
filesystem-level (beds share the host kernel) — a good fit for **trusted or
semi-trusted** code; for **untrusted** code you want stronger isolation (a
microVM or a dedicated VM/container).

## Runtime model

- **A bed is one durable sandbox identity**: its workspace and lifecycle survive
  replacement of the process realm that currently serves it.
- **An Executor is a bed's current process realm**: it owns command and session
  processes and can be replaced without replacing the bed. An **Execution** is
  one command run and records both its bed id and executor id.
- A long-running shell exists only for an explicit `/session`; ordinary
  `/command` calls each run in a fresh process.
- **Default bed**: a request without a bed id lands on `default`, so if you only
  need one sandbox you can ignore beds entirely.
- **Choosing a bed**: send the HTTP header `X-Hostel-Bed` (or `?bed=`); empty
  means the default. Beds are isolated from each other — one bed's shell and
  files are invisible to another.

## Quick start

```bash
make build
./bin/hostel --isolation dorm --workspace-root ./.workspace --addr :8872

curl -s localhost:8872/ping                                   # pong
curl -s localhost:8872/healthz | jq
# foreground command (SSE stream)
curl -sN -XPOST localhost:8872/command \
  -H 'Content-Type: application/json' -d '{"command":"echo hi > /workspace/a.txt; cat /workspace/a.txt"}'
# read the file back
curl -s 'localhost:8872/files/download?path=/workspace/a.txt'
# target another bed (a separate isolation unit; cannot see the default bed's files)
curl -s 'localhost:8872/files/info?path=/workspace/a.txt' -H 'X-Hostel-Bed: conv-1'
```

## End-to-end tests

`make e2e` starts a real local Hostel binary and verifies the public runtime,
bed lifecycle, file/command, and isolation contracts. Image publishers can run
the PyPI/npm/Chromium userland contract with
`make e2e-image E2E_IMAGE=<image>`. See [tests/e2e/README.md](tests/e2e/README.md)
for required host capabilities and release-gate options.

## API (v1)

| Group | Endpoints |
|---|---|
| Basic | `GET /ping`, `GET /healthz` |
| Metrics | `GET /metrics`, `GET /metrics/watch` (SSE) |
| Files | `GET /files/info`, `DELETE /files`, `POST /files/mv`, `POST /files/permissions`, `GET /files/search`, `POST /files/replace`, `POST /files/upload`, `GET /files/download` |
| Directories | `GET /directories/list`, `POST /directories`, `DELETE /directories` |
| Command | `POST /command` (SSE), `DELETE /command`, `GET /command/status/:id`, `GET /command/:id/logs` |
| Session | `POST /session`, `POST /session/:id/run` (SSE), `DELETE /session/:id` |
| Isolated session | `/v1/isolated/session(s)`, `run` (SSE), session-scoped files/directories, `capabilities` |
| Beds | `GET/POST /v1/beds`, `GET/DELETE /v1/beds/:id`, `POST /v1/beds/:id/checkpoint`, `GET /v1/beds/capabilities` |
| Scheduler | `GET /v1/beds` — instance capacity, state counts + every local bed's (resident + dormant) lifecycle, generation and retention |

The isolated-session resource model maps one session directly to one non-default
bed, so it does not introduce a second lifecycle object. Its run stream uses the
same hostel-native execution events as `/command`. The default bed only serves
requests that omit a bed id and is never listed or attached as an isolated
session. Creation currently supports the balanced profile with the bed-owned
read-write `/workspace` and shared network; unsupported isolation options are
rejected instead of being silently ignored. Diff and commit report
`NOT_SUPPORTED`.

Metrics follow the selected bed (`X-Hostel-Bed` / `?bed=`): with delegated
cgroup v2, CPU usage and current memory come from that bed's accounting group,
while CPU count and total memory describe the shared carrier capacity. No
limits are applied. On hosts without delegated cgroup v2, the same response
falls back to execd-compatible instance metrics; `/healthz` and capabilities
report the active `resource_accounting` backend.

Path semantics are owned by the Bed's **BedFS**. The bed is picked by the
`X-Hostel-Bed` header first; after that the bed behaves as if it owned the whole
filesystem. The client's `/` is the bed_home, so every absolute path lands inside the bed by one
rule (`/tmp/job` → `<bed_home>/tmp/job`, `/workspace/a` →
`<bed_home>/workspace/a` — `/workspace` is a real subdir, not an alias), and
relative paths are workspace-relative per the OpenSandbox SDK contract. The
mapping is one-to-one: responses echo paths exactly as you sent them. A bed
never sees the host. One consequence to be aware of:

- Structured fields such as file `path` and command `cwd` always use BedFS;
  `cwd: "/"` therefore means bed_home on every isolation level.
- **Command text is not rewritten**: an absolute literal inside a shell command
  (`cat /tmp/job/a.txt`) is resolved by the bed's process view, not by this
  mapping. Use `cwd` + relative paths to address files written via the file API.

Under `bwrap`, the complete bed_home has a mechanism-private Executor mount and
the workspace is additionally mounted at the stable `/workspace`, so any BedFS
cwd is usable while workspace shell paths keep their canonical spelling. Under
`direct` (no mount namespace) an Executor uses the carrier BedFS paths. Probe
the `workspace_mount` capability only when command text depends on a literal
`/workspace`; it is not a BedFS capability flag. See `docs/filesystem.md`.

## Isolation

Data isolation is graded by **hostel room type**: `--isolation
dorm|room|suite|auto` (default `auto` = the environment ceiling). The effective
level is `min(requested, ceiling)` — an over-ask degrades honestly, a lower ask
is a deliberate downgrade.

- `dorm` (bunk): chdir only, no enforced isolation (= direct, all platforms);
- `room` (private room, shared toilet): Landlock LSM — a bed can't *access*
  other beds' data (EACCES) but siblings stay visible and `/tmp` / system paths
  are shared; **no capability required** (Linux ≥5.13);
- `suite` (fully private): bwrap mount ns — siblings invisible + private `/tmp`
  + canonical `/workspace` mount (needs userns or CAP_SYS_ADMIN).

The environment ceiling is probed at boot; healthz/capabilities report
`isolation.{level,mechanism,requested,effective,ceiling}`. See
`docs/data.md`.

Stronger isolation (real setuid, seccomp, per-bed CPU/memory limits via cgroups,
copy-on-write overlay workspaces, PTY over WebSocket) is tracked in
`docs/backlog.md`.

## Managed services (Chromium / Jupyter / …, planned)

Some tools are heavy to start but can serve many tenants at once — a browser, a
Jupyter server. hostel will run one shared instance and give each bed its own
slice using the tool's native mechanism (a browser context per bed, a kernel per
bed), with outputs saved into that bed's workspace. v1 wires the teardown hook
(a bed's slices are released when the bed is deleted or times out); the actual
Chromium/Jupyter integrations come later.

## Amenities (shared facilities)

Heavyweight, natively multi-tenant tools run **once** per hostel and are sliced
per bed. The first is **Chromium**: one shared browser, an isolated
BrowserContext per bed, artifacts saved into the bed workspace. Enable by
shipping a chromium binary (`--chromium-path`, or it's probed) or attaching to
an existing instance (`--chromium-cdp-url`). Bed-scoped verbs (the raw CDP
socket is never exposed):

```
POST /v1/beds/:id/browser/goto        {url}
POST /v1/beds/:id/browser/screenshot  {path?}   # saved under the bed workspace
POST /v1/beds/:id/browser/text
POST /v1/beds/:id/browser/{click,type,press,scroll,wait}
POST /v1/beds/:id/browser/close
```

The browser starts on first use and stops after an idle grace; capabilities
reports `amenities: {chromium: idle|running}`.

## Configuration

Flags (or `HOSTEL_*` env vars): `--addr` / `--workspace-root` / `--isolation` /
`--dorm-read-fallback-root` / `--default-bed` / `--shell` / `--bed-idle-timeout` / `--max-beds` /
`--max-pinned-beds` / `--admission-cpu-threshold` / `--admission-memory-threshold` /
`--executor` / `--bed-env-passthrough` / `--store` /
`--s3-bucket` / `--s3-prefix` / `--s3-endpoint` / `--s3-path-style` / `--persist-interval` /
`--luggage-high-bytes` / `--luggage-low-bytes` /
`--chromium-path` / `--chromium-cdp-url` / `--chromium-idle-stop` / `--chromium-debug-port` /
`--enable-tracing`.

Dorm commands share the carrier mount namespace, so a command may write a
literal absolute path outside BedFS. On an exclusive carrier,
`--dorm-read-fallback-root /` (or `HOSTEL_DORM_READ_FALLBACK_ROOT=/`) lets
read-only file APIs retry that process path after the BedFS path is absent.
The option is disabled by default: it exposes the configured root to file API
reads and is unsafe when a carrier is shared. BedFS always wins when both paths
exist, and upload/replace/chmod/move/delete never use the fallback.

OpenTelemetry traces use `OTEL_EXPORTER_OTLP_TRACES_GRPC_ENDPOINT` or
`OTEL_EXPORTER_OTLP_TRACES_HTTP_ENDPOINT`; gRPC wins when both are set. Tracing
is disabled by default and enabled with `HOSTEL_ENABLE_TRACING=true` (or
`--enable-tracing`).

Environment namespaces follow ownership: `HOSTEL_*` configures the daemon and
is never inherited wholesale by bed processes; bed identity/capabilities use
`BED_*` (`BED_ID` is always present); ecosystem variables keep their standard
names. `--bed-env-passthrough` selects carrier software variables such as
`PATH`, locale, certificate, Python, npm and uv settings. Request `envs` are an
invocation-scoped overlay. Callers cannot claim the reserved `HOSTEL_*` or
`BED_*` namespaces.

Executor backend: `--executor auto` (default) probes the Linux `supervisor` backend
and otherwise uses `local`. Explicit `supervisor` fails startup when the backend cannot
serve; `local` explicitly keeps processes as direct hostel children. The supervisor
owns the whole Executor process tree, including `setsid`/double-fork descendants
that a plain process-group sweep cannot reach. Its RPCs are reconnectable,
`Start` is idempotent by process id, and a lost Executor is reported as a stable
`executor_lost` result rather than a raw socket EOF. See docs/kernel.md.

Bed initialization is asynchronous at the management boundary: `POST /v1/beds`
returns `202` with `status.phase=initializing`; poll `GET /v1/beds/:id` until
`status.readiness.status=true`. Snapshot inspection, restore, BedFS preparation,
and failures are exposed through readiness reason/message. Native data-plane
requests still create on first use by joining the same initialization and waiting
for Ready, so they never observe a partial BedFS.

Persistence: setting `--s3-bucket` (any S3-compatible endpoint) turns it on.

- The default `--store auto` stores new beds as immutable ~32 MiB pack files.
- Auto detects existing layouts for backward compatibility:
  - Existing CAS beds remain readable and can transition to pack.
  - Existing pack and tar beds keep their current layout.
- Explicit `s3` / `pack` / `tar` selections never inspect or migrate another
  layout. Tar always replaces one complete tar.gz and keeps one object per bed.
- Without a bucket, auto uses the no-op backend.

Snapshots restore when the bed is created again and persist on evict
(DELETE / idle reap) or explicit checkpoint. Normal
operations and pressure submit coalesced sync requests; the store loop owns
serialization, retry/backoff, and the optional `--persist-interval` safety net.
A bed's durable identity is the
snapshot; the local dir is just its working copy.
`DELETE /v1/beds/:id` evicts (identity kept); add `?purge=true` to also delete
the snapshot and end the identity. An evict raced by live traffic returns
`409 BED_BUSY` instead of dropping mid-flight writes.
Bucket addressing defaults to virtual-hosted style (required by TOS); set
`--s3-path-style` only for endpoints such as MinIO that require path-style.

Luggage: an evicted bed leaves its local dir behind as a warm cache — resuming
on the same instance skips the snapshot download (a monotonic generation
counter, carried in bed meta and snapshot metadata, decides freshness; a copy
that fell behind is discarded and re-restored). `--luggage-high-bytes` caps the
disk luggage may hold: past it, cold copies are deleted — stale generation
first, then least recently used — until under `--luggage-low-bytes` (default
80% of high). With the `noop` store luggage is the only copy, so luggage GC
there destroys data — same honesty rule as everywhere: `/healthz` tells you
which world you're in.

Capacity: `--max-beds N` caps resident tenant beds; `--max-pinned-beds M` is the
hard pinned-count limit. A bed is
pinned while an operation is in flight or its latest data has not reached the
durable store. With the noop store, only in-flight operations pin. `M=0` inherits `N`; both are
unlimited only when `N=0` too. The default bed is exempt from both. An explicit
`M` above a finite `N` is clamped to `N`: resident capacity is always the hard
ceiling for pinned capacity.
A pinned bed keeps its carrier commitment. At 80% of `M`, `GET /v1/beds`
reports soft `bed_pressure` so the upstream can warm capacity and avoid new
placements while retaining the final 20% for source-carrier fallback. At the
hard limit, new/dormant placement and unpinned-idle admission return retryable
`429 INSUFFICIENT_BED`; resident count exhaustion remains `429
BED_LIMIT_EXCEEDED`. `pinned` and `data_synced` are reported per bed, and the
capacity error includes the pinned/resident count and limit snapshot.

Carrier resource admission complements those count limits. Hostel samples its
container cgroup and refuses new ownership or an unpinned idle bed's first operation with `429
RESOURCE_PRESSURE` when recent CPU or current memory usage reaches
`--admission-cpu-threshold` / `--admission-memory-threshold` (percent, default
90; 0 disables that dimension). Pinned beds and the default bed keep
running. A missing cgroup, read error, or unlimited cgroup dimension fails open
to the count limits. `/healthz`, `GET /v1/beds`, and capabilities report the
finite cgroup limits, latest usage ratios, thresholds, and `accepting` verdict.

## Container image

`deploy/docker/Dockerfile` is a multi-stage build: a static, pure-Go hostel binary on a
`debian-slim` runtime that bundles the two optional facilities — a pinned,
non-setuid **bubblewrap**
(the `suite` level) and **chromium** (the browser amenity). Both stay optional:
hostel probes them at boot and degrades honestly, so a locked-down pod without
namespaces still serves.

```bash
make image                     # full image (bwrap + chromium), current arch
make image-lean                # bwrap only (~150MB); browser via --chromium-cdp-url or absent
make image-multiarch IMAGE=repo/hostel:tag   # linux/amd64 + arm64, pushed to a registry
docker run -p 8872:8872 hostel:dev
```

The build is multi-arch (`linux/amd64`, `linux/arm64`): the Go builder
cross-compiles natively, while the pinned bwrap source build and Debian runtime
run per target so their native dependencies match the image architecture.
`make image-multiarch` needs `docker buildx` and pushes directly (a
multi-platform image can't load into the local docker).

In-container defaults (all overridable via `HOSTEL_*`): `--isolation suite`,
`--workspace-root /workspace` (a declared volume), `--chromium-path
/usr/bin/chromium`. `tini` is PID 1 (reaps shell/chromium children); the
`HEALTHCHECK` calls `hostel --health` (self-GETs `/healthz`, no curl needed).
Whether bwrap actually isolates depends on the pod granting user namespaces /
`CAP_SYS_ADMIN`; without them hostel logs the degrade and runs at `dorm`. The
image runs as root by default (bwrap mount setup + chromium `--no-sandbox`);
harden with a dropped-capability `securityContext` per deployment.

## License & acknowledgements

hostel is licensed under **Apache-2.0** (see [`LICENSE`](LICENSE)), consistent
with its origin. It is **based on / derived from OpenSandbox execd**
(https://github.com/alibaba/opensandbox, Apache-2.0): it began as a
reimplementation of that project's isolated-execution model and is expected to
diverge over time. See [`NOTICE`](NOTICE) for attribution details.
