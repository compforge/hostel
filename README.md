# hostel

English | [简体中文](README.zh-CN.md)

**hostel is an agent-native sandbox runtime.** It manages many sandboxes with
best-effort isolation from a single process and exposes an HTTP API to create them, run commands and
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

hostel takes a lighter approach: manage many **beds** from one process.
A bed is near-instant to create and costs almost nothing while idle, so a single
machine or container can hold a large number of them. Beds share the host kernel;
file, network and resource boundaries depend on available mechanisms. This fits
**trusted or semi-trusted** code; for **untrusted** code you want stronger isolation (a
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
  means the default. Commands and file APIs target that bed; cross-bed visibility
  depends on the active isolation mechanisms. The caller must enforce API access authorization.

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
# target another bed: the file API resolves paths within that bed
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
| Basic | `GET /ping`, `GET /healthz`, `GET /v1/diagnostics` |
| Metrics | `GET /metrics`, `GET /metrics/watch` (SSE) |
| Files | `GET /files/info`, `DELETE /files`, `POST /files/mv`, `POST /files/permissions`, `GET /files/search`, `POST /files/replace`, `POST /files/upload`, `GET /files/download` |
| Directories | `GET /directories/list`, `POST /directories`, `DELETE /directories` |
| Command | `POST /command` (SSE), `DELETE /command`, `GET /command/status/:id`, `GET /command/:id/logs` |
| Session | `POST /session`, `POST /session/:id/run` (SSE), `DELETE /session/:id` |
| Isolated session | `/v1/isolated/session(s)`, `run` (SSE), session-scoped files/directories, `capabilities` |
| Transfers | `POST /v1/beds/:id/transfers`, `GET/DELETE /v1/beds/:id/transfers/:transfer_id` — direct Bed ↔ S3 file copies; [contract](docs/transfers.md) |
| Beds | `GET/POST /v1/beds`, `GET/DELETE /v1/beds/:id`, `POST /v1/beds/:id/checkpoint`, `GET /v1/beds/capabilities` |
| Scheduler | `GET /v1/beds` — instance capacity, state counts + every local bed's (resident + dormant) lifecycle, generation and retention |

`POST /command` accepts an optional `stdin` string in both foreground and
background mode. It is delivered unchanged to the process, followed by EOF;
omitting it or sending an empty string supplies immediate EOF. For example,
`{"command":"cat > result.txt","stdin":"hello\n"}` writes the supplied text
without embedding it in shell source. A command may exit without consuming all
input; its exit code still determines the result.

The isolated-session resource model maps one session directly to one non-default
bed, so it does not introduce a second lifecycle object. Its run stream uses the
same hostel-native execution events as `/command`. The default bed only serves
requests that omit a bed id and is never listed or attached as an isolated
session. Creation currently supports the balanced profile with the bed-owned
read-write `/workspace`; network sharing follows the instance network capability; unsupported isolation options are
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
cwd is usable while workspace shell paths keep their canonical spelling. This
is Hostel's built-in projection from BedFS `/workspace`
(`<bed_home>/workspace`) to Executor `/workspace`. It uses the same projection
model as configured paths but is intentionally not repeated in
`HOSTEL_PROJECTED_PATHS`. A deployment can add multiple business-neutral
projections with
`HOSTEL_PROJECTED_PATHS`, for example `/memory=/mnt/memory,/cache=/mnt/cache`.
Under dorm/room, Hostel discovers `proot` and `pathshim` through `PATH`, then
smoke-tests their complete projection sets. PRoot is preferred when ptrace and
its own smoke pass; pathshim is next; otherwise commands use Carrier paths.
These compatibility views do not change the isolation level. See
`docs/filesystem.md`.

Store durability is independently controlled by `HOSTEL_PERSISTED_PATHS`, a
comma-separated BedFS path allowlist whose default is `/workspace`. Adding a
projection never makes its source durable unless it is explicitly added here.

## Isolation

A Bed aims to provide an independent execution space for files, processes,
networking and resources. Hostel provides as much isolation as the environment
supports and reports the actual boundaries. Shared infrastructure keeps the
runtime lightweight; available mechanisms determine which boundaries are enforced.

File isolation is graded by room type: `--isolation dorm|room|suite|auto`
(`auto` selects the environment ceiling; a higher request degrades to that ceiling).

- `dorm`: logical separation without an enforced cross-Bed file boundary.
- `room`: Landlock or a separate UID restricts access to other Beds' data;
  directory existence and shared system paths remain visible.
- `suite`: bwrap provides a private mount view, hides sibling workspaces and
  mounts the Bed's workspace at `/workspace`.

These grades describe file isolation. Network namespaces are probed independently
and currently cover Bed commands and shells; shared Chromium/MCP egress still uses
the Carrier. Per-Bed CPU/memory accounting does not imply hard limits. PRoot and
pathshim improve process path compatibility without adding a security boundary.

Health and capability responses report the selected mechanisms, scope and reasons
for unavailable capabilities. See [the isolation design](docs/isolation.md) for
the full model and [the backlog](docs/backlog.md) for remaining gaps.

## Amenities (shared facilities)

Heavyweight, natively multi-tenant tools run **once** per hostel and are sliced
per bed. **Chromium** uses one shared browser with a BrowserContext per bed
and artifacts saved into the bed workspace. Enable by
shipping a chromium binary (`--chromium-path`, or it's probed) or attaching to
an existing instance (`--chromium-cdp-url`). Hostel exposes Bed-scoped verbs
without handing out the raw browser-level CDP endpoint:

```
POST /v1/beds/:id/browser/goto        {url}
POST /v1/beds/:id/browser/screenshot  {path?}   # saved under the bed workspace
POST /v1/beds/:id/browser/text
POST /v1/beds/:id/browser/{click,type,press,scroll,wait}
POST /v1/beds/:id/browser/close
```

The browser starts on first use and stops after an idle grace; capabilities
reports its lifecycle state in `amenities`. Bed-scoped CDP proxy endpoints are
also available for Playwright clients; filtering is limited and does not promise
adversarial isolation. Browser traffic still uses the Carrier network.
[MCP](docs/mcp.md) manages remote connections per bed; Jupyter is not implemented.
See [shared facility boundaries](docs/amenity.md).

## Configuration

Flags (or `HOSTEL_*` env vars): `--addr` / `--workspace-root` / `--isolation` / `--projected-paths` / `--persisted-paths` /
`--dorm-read-fallback-root` / `--default-bed` / `--shell` / `--bed-idle-timeout` / `--max-beds` /
`--max-pinned-beds` / `--bed-pressure-threshold-percent` / `--admission-cpu-threshold` / `--admission-memory-threshold` /
`--executor` / `--bed-uid` / `--bed-gid` / `--sync` /
`--s3-bucket` / `--s3-prefix` / `--s3-endpoint` / `--s3-path-style` / `--s3-region` / `--persist-interval` /
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

OpenTelemetry traces use `HOSTEL_OTEL_TRACES_GRPC_ENDPOINT` or
`HOSTEL_OTEL_TRACES_HTTP_ENDPOINT`; gRPC wins when both are set. Tracing
is disabled by default and enabled with `HOSTEL_ENABLE_TRACING=true` (or
`--enable-tracing`).

Environment namespaces follow ownership: `HOSTEL_*` configures the daemon and
is filtered from bed processes; externally supplied `BED_*` and the managed CDP
endpoint are filtered as well, then Hostel injects the actual bed context.
Every other Carrier variable is inherited by default, including ecosystem and
deployment-specific variables. The deployment owner is responsible for the
safety of those inherited values. Request `envs` are an invocation-scoped
overlay and cannot claim the reserved `HOSTEL_*` or `BED_*` namespaces.

S3 configuration is Hostel-owned and uses `HOSTEL_S3_REGION`,
`HOSTEL_S3_ACCESS_KEY_ID`, `HOSTEL_S3_SECRET_ACCESS_KEY`, and optionally
`HOSTEL_S3_SESSION_TOKEN`; credentials are environment-only and have no CLI
flags.

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

The optional S3 backend defines where snapshots are stored (`--s3-bucket` and
other S3 connection settings). `--sync` / `HOSTEL_SYNC` chooses how to synchronize and organize
those snapshots; local data remains in BedHome.

For direct file copies between a Bed and S3, use the [transfers API](docs/transfers.md).
It works with `sync=noop`, supports progress and cancellation, and addresses objects relative to the configured S3 prefix. The existing `/files/*` APIs handle file bytes
passed through the HTTP client.

- The default `--sync auto` stores new beds as immutable ~32 MiB pack files.
- Auto detects existing layouts for backward compatibility:
  - Existing CAS beds remain readable and can transition to pack.
  - Existing pack, tar and restic beds keep their current layout.
- Explicit `cas` / `pack` / `tar` / `restic` selections write the chosen layout and use
  auto detection to read the newest snapshot across layouts. Switching formats
  continues the existing generation sequence; purge removes all layouts.
  Tar always replaces one complete tar.gz and keeps one object per bed.
- Without a bucket, every valid store policy is effectively noop.

A Bed can opt out of automatic persistence while sharing a configured S3
instance with other Beds:

```http
POST /v1/beds
Content-Type: application/json

{"id":"externally-managed","sync":"noop"}
```

Explicit `sync` values (`noop`, `auto`, `cas`, `pack`, `tar`, `restic`) take
precedence over `HOSTEL_SYNC`. Omission
reuses a resident Bed or its local metadata; a new Bed inherits the default.
Eviction removes the local metadata, so repeat the override when recreating a
Bed or moving it to another instance. Changing an active Bed's policy returns
`409 BED_SYNC_CONFLICT`. See [Store](docs/store.md).

Snapshots restore when the bed is created again and persist on evict
(DELETE / idle reap) or explicit checkpoint. Normal
operations and pressure submit coalesced sync requests; the store loop owns
serialization, retry/backoff, and the optional `--persist-interval` safety net.
A bed's durable identity is the
snapshot; the local dir is just its working copy.
`DELETE /v1/beds/:id` evicts (durable snapshots remain; noop keeps no data).
Add `?purge=true` to delete the snapshot as well. If the Bed has no local metadata,
repeat its override, for example `?purge=true&sync=noop`; omission uses the instance default.
An evict raced by live traffic returns
`409 BED_BUSY` instead of dropping mid-flight writes.
Bucket addressing defaults to virtual-hosted style (required by TOS); set
`--s3-path-style` only for endpoints such as MinIO that require path-style.

Successful idle eviction removes the local Bed directory for every Store
policy. A durable Store persists first and the next placement restores the
snapshot; noop performs no persistence and the next placement starts fresh.
Luggage scanning only covers orphaned directories left by an unclean shutdown
or older Hostel version.

Capacity has three named aggregate counts:

- `occupied_beds`: initializing plus resident/evicting tenant Beds; this is the
  count governed by the hard `--max-beds N` limit.
- `resident_beds`: resident/evicting tenant Beds prepared on this node.
- `pinned_beds`: the resident subset that is running work or whose latest data
  has not reached the durable store.

With the noop store, only in-flight operations pin. `--max-pinned-beds M` is a
pressure reference, not an admission limit. `M=0` inherits `N`; both references
are disabled only when `N=0` too. The default bed is exempt from all three counts.
A pinned Bed keeps its carrier commitment.

`--bed-pressure-threshold-percent` configures a shared high watermark (default
80, 0 disables). `GET /v1/beds` reports `bed_pressure=true` when either
`occupied_beds / max_beds` or `pinned_beds / max_pinned_beds` reaches that
watermark. Pressure only guides upstream placement; it never rejects Bed work,
and `pinned_beds` may exceed `max_pinned_beds`. Only a full `occupied_beds`
capacity returns `429 BED_LIMIT_EXCEEDED` for a new Bed.

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
`debian-slim` runtime that bundles pinned **bubblewrap**, **PRoot**, and
**pathshim** isolation helpers plus optional **chromium**. The helpers stay
optional at runtime: Hostel discovers them through `PATH`, probes the actual
kernel/runtime behavior, and degrades honestly when a locked-down Pod denies
the required operation.

```bash
make image                     # full image (helpers + chromium), current arch
make image-lean                # helpers only; browser via --chromium-cdp-url or absent
make image-multiarch IMAGE=repo/hostel:tag   # linux/amd64 + arm64, pushed to a registry
docker run -p 8872:8872 hostel:dev
```

The build is multi-arch (`linux/amd64`, `linux/arm64`): the Go builder
cross-compiles natively, while the pinned helper source builds and Debian
runtime run per target so their native dependencies match the image architecture.
`make image-multiarch` needs `docker buildx` and pushes directly (a
multi-platform image can't load into the local docker).

In-container defaults (all overridable via `HOSTEL_*`): `--isolation suite`,
`--workspace-root /workspace` (a declared volume), `--chromium-path
/usr/bin/chromium`. A root daemon keeps the privileges needed for mount,
network and Store operations, while Bed commands run as UID/GID 1000 by
default; `HOSTEL_BED_UID` and `HOSTEL_BED_GID` select another non-root fixed
identity. `tini` is PID 1 (reaps shell/chromium children); the
`HEALTHCHECK` calls `hostel --health` (self-GETs `/healthz`, no curl needed).
Whether bwrap actually isolates depends on usable user namespaces and mount
policy; PRoot depends on usable ptrace. Without either, Hostel logs the degrade
and keeps serving through the next supported workspace view. The
image daemon runs as root by default (bwrap mount setup + chromium
`--no-sandbox`); grant only the capabilities required by the selected
deployment features. The daemon/Bed identity model and capability matrix are
defined in [`docs/privilege.md`](docs/privilege.md).

## License & acknowledgements

hostel is licensed under **Apache-2.0** (see [`LICENSE`](LICENSE)), consistent
with its origin. It is **based on / derived from OpenSandbox execd**
(https://github.com/alibaba/opensandbox, Apache-2.0): it began as a
reimplementation of that project's isolated-execution model and is expected to
diverge over time. See [`NOTICE`](NOTICE) for attribution details.
The image aggregates PRoot as a separate GPL-2.0 program and ships its license,
modification notice, and corresponding modified source under
`/usr/share/doc/proot/`.

## Remote MCP tools

Hostel can proxy remote SSE and Streamable HTTP MCP tools for each bed. Configure
`PUT /v1/mcp/config`, then use `POST /v1/mcp/servers/{name}/tools/list` or
`POST /v1/mcp/servers/{name}/tools/call`, selecting the bed with `X-Hostel-Bed`.
Connections are reused within a bed and released when it is evicted. These are
trusted control-plane endpoints, with the same access boundary as `/command`.
See [MCP configuration, lifecycle and embedding](docs/mcp.md).

### Optional network namespaces

Hostel probes per-Bed networking at startup. When the complete probe succeeds,
commands and persistent shells use a private IPv4 network namespace with routed
outbound connectivity. Otherwise networking stays shared and Hostel still starts.
`GET /v1/diagnostics` and `/healthz` expose `network.enabled`, `backend`, `scope`,
and the probe failure reason. Shared Chromium traffic is not covered.
See [network management](docs/network.md) for prerequisites and boundaries.

Restic directory transfers use `sync: "restic"` and return a snapshot `ref`; the image includes restic 0.19.1. See [file transfers](docs/transfers.md) for repository addressing, credentials, and cancellation.
