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

The core profile covers health and capability reporting, asynchronous bed
creation, inventory invariants, capacity, foreground/background execution,
status/log/interrupt control, stateful and isolated sessions, the complete
file/directory mutation round trip, file/command interoperability, cross-bed
access, active-bed eviction safety, dormant luggage resume, purge, and the
dorm/room/suite isolation resolution available on the host.

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

The binary and image variables are mutually exclusive. Image mode is intended
for a Linux runner with Docker because the target container uses host networking
to reach the test-owned browser fixture.
