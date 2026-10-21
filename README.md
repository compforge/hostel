# hostel

English | [简体中文](README.zh-CN.md)

**Hostel is a sandbox runtime for AI agents.** Think of Hostel / Bed as a lightweight
take on kubelet / Pod: one daemon manages multiple sandboxes, called **beds**,
on a laptop, VM, CI runner or in a container, making a best effort to meet each
bed's needs within the host's capabilities.
Each bed declares shared resources and named execution environments called **pillows**.
A pillow holds environment variables, a working directory and volume mounts; commands,
shell sessions and an optional supervised main process run in that environment.
The first pillow is the default. See [the runtime model](docs/kernel.md) and [Pillow](docs/pillow.md).

Beds share a machine or container, reducing the need to provision a separate
runtime for every task. Hostel manages execution and local resources. In a cluster,
an upstream control plane owns instance placement, cross-instance routing and API authorization.

## What you can do

- Run commands with streamed output, cancellation and structured exit results;
  use shell sessions when state must persist across commands.
- Read, write and transfer files within a bed's workspace.
- Declare Pillow main processes with readiness checks and restart policies.
- Share bed volumes through explicit mounts in each pillow.
- Persist managed volumes to S3-compatible storage and restore them when a bed is recreated.
- Share Chromium and remote MCP connections with application state scoped to beds.

A bed's workspace survives replacement of its execution processes. Persistence
across eviction requires a configured Store; without it, eviction removes local data.

## Quick start

From a source checkout with Go and Make installed:

```bash
make build
./bin/hostel --isolation dorm --beds-root ./.workspace --addr 127.0.0.1:8872
```

In another terminal, declare a Bed, run a command and download its output file:

```bash
curl -fsS http://localhost:8872/v1/beds \
  -H 'Content-Type: application/json' \
  -d '{"id":"agent-1","pillows":[{"name":"agent","mounts":[{"volume":"workspace","path":"/workspace"}]}]}'

curl -fsSN http://localhost:8872/command \
  -H 'Content-Type: application/json' \
  -H 'X-Hostel-Bed: agent-1' \
  -d '{"command":"echo hi > hello.txt; cat hello.txt","cwd":"/workspace"}'

curl -fsS 'http://localhost:8872/files/download?path=/workspace/hello.txt' \
  -H 'X-Hostel-Bed: agent-1'
```

Create a Bed with at least one explicit Pillow before accessing it. Data requests
wait for initialization and return 404 for undeclared Beds. Use the same
`X-Hostel-Bed` header for subsequent requests; omitting the Bed selector returns 400. The daemon starts with no Beds; `default` is an ordinary name that must be explicitly declared.
Ordinary commands run in fresh processes; `/session` creates a persistent shell.
Use `cwd` and relative paths in commands: file API paths are bed-relative, while
absolute paths inside shell text follow the selected process environment.

Bed management responses expose Hostel's generated local lifetime `id` and the
caller-owned routing `name`. Recreating a destroyed Bed changes its ID; restarting
with retained local data preserves it. Request `id` parameters and Bed
paths accept Name. Route using the response `name`; see [the public Bed contract](docs/protocol.md).

For a local container build:

```bash
make image
# Add deployment permissions only for the isolation features you require.
docker run --rm -p 127.0.0.1:8872:8872 hostel:dev
```

The image includes filesystem helpers and Chromium. `make image-lean` builds
without Chromium. Available isolation depends on host and container permissions.

## Isolation and access

`--isolation` requests an instance-wide profile:

| Profile | Requested boundaries |
|---|---|
| `dorm` | Files organized per bed, with shared process-side access |
| `room` | Restricted cross-bed file access |
| `suite` | Private filesystem views and restricted cross-bed file access |
| `auto` | Request suite and allow degradation to supported mechanisms |

Room types summarize file isolation. Network, identity, PID namespaces and resource
controls are additional capabilities, reported independently; shared networking does
not lower a suite. Explicit requirements must be fulfilled.

Check `/healthz` and `/v1/status` for actual capabilities. Profiles depend on the
host; shared Chromium/MCP traffic uses the carrier network, and per-bed resource
accounting does not impose CPU or memory limits. Put the API behind a trusted
access boundary. For hostile workloads, use an appropriately isolated carrier
such as a dedicated VM or microVM.

## License and acknowledgements

Hostel is licensed under [Apache-2.0](LICENSE). Its resource and file APIs draw
on [OpenSandbox execd](https://github.com/alibaba/opensandbox); command execution
uses Hostel's own protocol. See [NOTICE](NOTICE) for attribution.
The container image includes PRoot as a separate GPL-2.0 program, with its license,
modification notice and corresponding modified source in `/usr/share/doc/proot/`.
