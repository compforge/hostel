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
backup integrity. Unit Store tests cover the persistence formats.

By default, a host may honestly degrade an unavailable isolation request. A
release runner can require levels to be realized instead:

```sh
HOSTEL_E2E_REQUIRE_ISOLATION=dorm,room,suite make e2e
```

To exercise the best-effort dorm/room `/workspace` process view, provide a
Linux pathshim binary. The isolation suite then requires canonical cwd, file
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

These variables are test-harness inputs: the launcher prepends their parent
directories to the target `PATH`. Hostel itself has no per-helper path setting.

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
| `HOSTEL_E2E_PROOT` | PRoot binary used to verify the ptrace-based `/workspace` fallback. |

The binary and image variables are mutually exclusive. Image mode is intended
for a Linux runner with Docker because the target container uses host networking
to reach the test-owned browser fixture.
