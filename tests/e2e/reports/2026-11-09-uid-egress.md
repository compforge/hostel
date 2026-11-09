# Shared-netns UID egress validation

Validated the uncommitted UID-policy worktree based on Hostel `5385e0c0`; runtime
VERSION is bumped to `0.2.10`. No image publication or customer deployment was performed.
The existing E2E suite owns public API assertions; the Host network package owns
real-kernel rule/DNS tests. These are separate validation boundaries.

## Execution conditions

- Runner: Linux amd64 container on devbox, kernel `5.15.120.bsk.3-amd64`.
- Userland: existing Bedbox 0.1.3 image, ID
  `sha256:9e7b8571e4375d9b7720c61bb90ceb3e86520d31a31641b158885362773d4fd3`.
- UID profile: root daemon, Docker default capabilities plus NET_ADMIN, default
  seccomp, container-local `net.ipv4.ip_forward=0`; no SYS_ADMIN addition.
  Fixed `HOSTEL_BED_UID/GID` were removed for the dedicated-UID cases.
- Fixed-UID profile: the forwarding test explicitly selects UID/GID 1000 and
  confirms unenforced policy. Required netns still rejects forwarding-disabled startup.
- Private-netns regression: a separate privileged disposable container with
  `ip_forward=1`. No host networking or host sysctl changes.
- Test binaries were cross-compiled using the build commands from the existing
  Kubernetes E2E Dockerfile, then mounted under `/usr/local/lib/hostel-e2e`, where
  the Bed's runtime file view can access subprocess probes.
- All named test containers were removed; the final container inventory was empty.

Test artifact SHA-256:

| Artifact | SHA-256 |
|---|---|
| hostel-e2e | `3c75b2dc7a71e91c59c8ce5093b0d2f8809ae2412db4adca88dc867bbab4cb98` |
| e2e.test | `77d80c6bf2dbe84bad5b1d2be77003785489110cb45b6e5999720518531d0079` |
| network.test | `569f429e0418539fb3ed810e6ecd1dfda34b3ec98ee7775418cba77ec650642b` |

## Results

| Test boundary | Native result |
|---|---|
| `TestUIDPolicyTraffic` with `HOSTEL_UID_POLICY_TEST=enabled` | PASS, 5.59s |
| `TestSharedUIDNetworkPolicyTraffic/local` | PASS, 6.46s |
| `TestSharedUIDNetworkPolicyTraffic/supervisor` | PASS, 6.85s |
| `TestCarrierForwardingDisabled` (fixed UID local/supervisor + required netns) | PASS |
| `TestFeaturePoliciesOff` | PASS |
| `TestLinuxPolicySeparatesNetworksAndRevokesAccess` | PASS, 2.38s |
| `TestNetworkPolicyTraffic/local` (private netns) | PASS, 7.44s |
| `TestNetworkPolicyTraffic/supervisor` (private netns) | PASS, 7.36s |

UID traffic assertions cover TCP/UDP allow and deny, UDP/TCP DNS interception,
per-UID DNS address grants, direct cross-UID resolver denial, IPv6 blocking,
revocation of an established TCP stream, invalid updates, duplicate UID exclusion,
and same-UID reacquisition without an old handle deleting the new policy.
API tests verify shared netns with distinct actual UID allocations, initial
BedSpec policy, session updates, replacement/merge/delete, status, and recreation.
The subprocess helper is skipped in the parent test run and executed by the traffic
case under each selected UID; that skip is not missing traffic coverage.

The E2E command corresponds to the documented `make e2e` suite selection:

```sh
HOSTEL_E2E_REQUIRE_UID_POLICY=1 HOSTEL_E2E_REQUIRE_FORWARDING_DISABLED=1 \
  make e2e E2E_ARGS='-run "^(TestSharedUIDNetworkPolicyTraffic|TestCarrierForwardingDisabled|TestFeaturePoliciesOff)$" -timeout 5m'
```

Execution here used the compiled `e2e.test` runner directly inside the disposable
container. Root/protocol lint and macOS full `go test -race` ran through
`repocli validate`; the subsequently added UID tool-selection test passed focused
race validation and full lint through the same entrypoint.

## Failed rounds and limits

The first kernel round exposed UDP OUTPUT drop returning EPERM synchronously;
the assertion now accepts that specific errno for UDP, alongside timeout, after
an allowed control UID proves the fixture. Connection-refused errors still fail.
The first API round decoded Bed creation's object-valued status as a policy string;
the helper now decodes policy bodies only at the policy endpoint. The next round
could not execute a probe mounted outside the Bed's runtime view; placing the
runner under `/usr` fixed the fixture without changing runtime access policy.
Local raw API logs, including failed rounds, are retained under
`runs/uid-policy-20261109/` (ignored, not published).

This run does not establish compatibility on the customer's 5.10 kernel or 4.19,
customer deployment behavior, shared browser/daemon-originated egress, or abnormal
daemon-termination recovery. The tested UID isolation requires actual per-Bed UIDs;
a fixed UID injected by an image is intentionally not overridden by this change.
