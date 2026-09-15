# hostel

English | [简体中文](README.zh-CN.md)

**Hostel is a sandbox runtime for AI agents.** One daemon manages multiple
sandboxes, called **beds**, on a laptop, VM, CI runner or in a container.
Each bed provides a workspace for commands, shell sessions and managed services.

Beds share a machine or container, reducing the need to provision a separate
runtime for every task. Hostel manages execution and local resources. In a cluster,
an upstream control plane owns instance placement, cross-instance routing and API authorization.

## What you can do

- Run commands with streamed output, cancellation and structured exit results;
  use shell sessions when state must persist across commands.
- Read, write and transfer files within a bed's workspace.
- Declare long-running services with readiness checks and restart policies.
- Persist selected paths to S3-compatible storage and restore them when a bed is recreated.
- Share Chromium and remote MCP connections with application state scoped to beds.

A bed's workspace survives replacement of its execution processes. Persistence
across eviction requires a configured Store; without it, eviction removes local data.

## Quick start

From a source checkout with Go and Make installed:

```bash
make build
./bin/hostel --isolation dorm --beds-root ./.workspace --addr 127.0.0.1:8872
```

In another terminal, run a command and download its output file:

```bash
curl -fsSN http://localhost:8872/command \
  -H 'Content-Type: application/json' \
  -H 'X-Hostel-Bed: agent-1' \
  -d '{"command":"echo hi > hello.txt; cat hello.txt","cwd":"/workspace"}'

curl -fsS 'http://localhost:8872/files/download?path=/workspace/hello.txt' \
  -H 'X-Hostel-Bed: agent-1'
```

The first command creates the bed on demand. Use the same `X-Hostel-Bed` header
for subsequent commands and file requests; omitting it selects the `default` bed.
Ordinary commands run in fresh processes; `/session` creates a persistent shell.
Use `cwd` and relative paths in commands: file API paths are bed-relative, while
absolute paths inside shell text follow the selected process environment.

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
| `dorm` | Shared filesystem access, identity and network |
| `room` | Restricted cross-bed file access, dedicated bed identities, shared network |
| `suite` | Private filesystem views, dedicated bed identities and private bed networks |
| `auto` | Request suite and allow degradation to supported mechanisms |

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
