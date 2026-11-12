# hostel

English | [简体中文](README.zh-CN.md)

**Hostel is a stripped-down take on Docker for AI agents.** One daemon manages
multiple lightweight workspaces, called **Beds**, on a machine or inside a container.
It reuses the existing system and toolchain to provide files, commands, shell sessions
and supervised processes, with built-in data synchronization and shared facilities.

## What makes it stripped-down?

- **Reuse the software base.** Beds share the host or outer container's programs
  and dependencies; Hostel does not pull and boot a separate container image for each Bed.
- **Declare execution environments.** Configure environment variables, working
  directories, volume mounts and optional main processes, then run commands and
  services in those environments.
- **Work with the host's capabilities.** A complete set of namespaces, cgroups
  and mount permissions is not a prerequisite for every Bed. Hostel makes the most
  of available mechanisms, coordinates its subsystems and reports what each provides;
  callers decide whether the results meet their needs. Required tools and functional
  contracts such as read-only mounts must still work.
- **Share heavier facilities.** Beds can share Chromium and remote MCP connections,
  with application state scoped to each Bed, reducing duplicated processes and connections.

Hostel is useful when many agent tasks can share an existing software environment.
It manages execution, local resources and working data within one instance; an upstream
control plane owns instance placement, cross-instance routing and API authorization.

## Working with Beds

A **Bed** is a workspace with its own identity, lifecycle and shared resources.
It declares named **Volumes** for data and one or more **Pillows** for execution.
Each Pillow holds environment variables, a working directory and volume mounts;
commands, shell sessions and an optional supervised main process run in that environment.
The first Pillow is the default. Pillows within a Bed share its user identity, network and
resources; they are not separate container isolation boundaries.

Commands provide streamed output, cancellation and structured exit results. Shell
sessions preserve shell state across commands, while main processes support readiness
checks and restart policies. File APIs read, write, upload, download and transfer data
through the selected Pillow's mounts.

Replacing execution processes preserves the Bed's working data. To retain managed
volumes across eviction, declare them for persistence and configure S3-compatible storage.
Eviction removes Hostel-managed local data; external directories mounted through
`host_path` retain their externally managed lifecycle. See [Volumes](docs/volumes.md).

## Quick start

From a source checkout with Go 1.26+ and Make installed:

```bash
make build
./bin/hostel --beds-root ./.workspace --addr 127.0.0.1:8872
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

The command returns an SSE stream: look for `stdout` containing `hi` and an
`execution_end` result with `process.kind` set to `exited` and `process.exit_code`
set to `0`. The final download prints `hi`.

Create a Bed with at least one explicit Pillow before accessing it. A create response
of HTTP 202 means initialization has been accepted. Data requests wait for
initialization and return 404 for undeclared Beds. Use the same `X-Hostel-Bed`
header for subsequent requests; omitting the Bed selector returns 400.
Ordinary commands run in fresh processes; `/session` creates a persistent shell.
Use `cwd` and relative paths in commands: file API paths are bed-relative, while
absolute paths inside shell text follow the selected process environment.

In this example, the request's `id: "agent-1"` supplies the Bed's routing name.
Management responses return that value as `name` alongside Hostel's generated
local lifetime `id`. Recreating a destroyed Bed changes its ID; restarting
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

## Capabilities and integration

Check `/healthz` and `/v1/status` for actual capabilities. File isolation, process
views, networking and resource accounting depend on the host; per-Bed accounting
currently does not impose CPU or memory limits. Shared networking may retain
requested policies without enforcing them; status reports that gap. Shared Chromium/MCP traffic uses
the host or outer container's network. Hostel currently targets trusted or semi-trusted
workloads; put its API behind a trusted access boundary and choose an appropriately
isolated host or outer container for your workload. See [Isolation](docs/isolation.md) for configuration details.

The quick start uses Hostel's native API. OpenSandbox Execd adapters are available
under `/execd/...` and `/v1/beds/:bedId/execd/...`; code/context endpoints are not
supported. Go callers can import the independent `github.com/qiankunli/hostel/protocol`
module for Bed management data types. See [Protocols](docs/protocol.md) for routing,
compatibility and result contracts.

- [Bed lifecycle](docs/bed.md): declarations, readiness and data retention.
- [Pillows](docs/pillow.md): execution environments, sessions and supervised processes.
- [Volumes](docs/volumes.md): mounts, persistence and file paths.
- [Shared facilities](docs/amenity.md): Chromium and MCP integration.
- [Kubernetes examples](deploy/k8s/): deployment configuration.

## License and acknowledgements

Hostel is licensed under [Apache-2.0](LICENSE). Its resource and file APIs draw
on [OpenSandbox execd](https://github.com/alibaba/opensandbox); command execution
uses Hostel's own protocol. See [NOTICE](NOTICE) for attribution.
The container image includes PRoot as a separate GPL-2.0 program, with its license,
modification notice and corresponding modified source in `/usr/share/doc/proot/`.
