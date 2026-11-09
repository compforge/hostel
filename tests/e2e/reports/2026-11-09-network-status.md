# Network status contract validation

Validated the uncommitted network-status changes on `8c10c52a`, VERSION `0.2.13`.
The final contract has only isolation/egress dimensions, with no legacy field
projection. Policy queries and successful mutations return `EgressStatus` directly,
matching Bed detail's `network.egress`. See [the contract](../../../docs/network.md#状态契约).
No image was published and no customer or QA environment was deployed.

## Conditions

- Existing disposable image: `hostel-extra-gids:validation`, image ID
  `sha256:2fec9aa0648ee258b8a62a4705038352a54d40776adceafc6cc4f36343572707`.
- Debian 12, Linux amd64, kernel `5.15.120.bsk.3-amd64`.
- UID profile: Docker default capabilities plus NET_ADMIN, default seccomp,
  container-local `ip_forward=0`. The unenforced case explicitly disables UIDNFT.
- Private profile: privileged container, `ip_forward=1`.
- Focused follow-up: privileged container, `ip_forward=0`, ROOTFUL enabled;
  covers disabled tools and private filesystem with shared networking.
- No host networking or host sysctl mutation. Cases own their daemons, loopback
  HTTP endpoints and TCP fixtures. Colocated runners are mounted under `/usr`.
- Run artifacts use the devbox data disk; the root partition was full during an
  earlier setup attempt. That setup failure and earlier implementation evidence
  remain in `/private/tmp/hostel-network-status-20261109/`.

## Results and failed round

| Case | Native result |
|---|---|
| `TestCarrierForwardingDisabled` | PASS: local / supervisor / required |
| `TestSharedUIDNetworkPolicyTraffic` | PASS: local 6.59s / supervisor 6.83s |
| `TestNetworkPolicyTraffic` | PASS: local 7.20s / supervisor 7.44s |
| `TestPrivateNetworkCrashRecovery` | PASS: before-crash / after-crash, 3.75s |
| `TestNetworkNamespaces` | PASS: local / supervisor, 2.97s |
| `TestFeaturePoliciesOff` | Failed on obsolete mode assertion; corrected typed assertion PASS, 0.41s |
| `TestPrivateFilesWithSharedNetwork` | PASS: network-off / network-probe-failure, 2.24s |
| `TestPolicyTransactionFailureEvidence` | PASS: normal rejection / killed / canceled / unknown error |

The first UID suite verdict remains **fail** (`20261109T105541.446307181Z`):
FeaturePoliciesOff still searched the body for the removed `mode` field, despite
receiving the correct `state=unenforced`, `effective=null` response. Tests now
parse `protocol.EgressStatus`. The daemon was unchanged for the follow-up; its
privileged conditions are recorded separately, not presented as the original UID profile.

Private suite verdict: **pass** (`20261109T105603.328644044Z`). Focused follow-up
verdict: **pass** (`20261109T105841.450572054Z`). No selected cases skipped.

Traffic assertions verify initial/default publication before GET, agreement of
Bed detail and policy responses, allow/deny, live session updates, sibling
independence and recreation. Recovery verifies replacement namespaces and policy
updates after restart. Mixed isolation asserts independent room/egress outcomes.
Unit regressions cover uncertain commits, DNS fencing, read-only queries, detached
snapshots and retained cleanup ownership. Real lost nft acknowledgement injection
was not exercised through HTTP; subprocess error classification is a separate test.

Root/protocol full lint, impact-selected root race tests and full protocol race
tests passed through `repocli validate`. macOS build and Linux amd64 daemon,
E2E and host-network test compilation passed.

## Artifact identity and cleanup

| Artifact | SHA-256 |
|---|---|
| hostel-e2e (all simplified-contract runs) | `bd9e57a9f19b9fcb51f7dab408e0d03045e95295e0eadafc4cade64cd9f06fb3` |
| e2e.test (UID/private first round) | `abdea997651f645f2aec70a8a17205c92680cf495276c2aeb78c65e4ab67056c` |
| e2e.test (corrected follow-up) | `e443186919510a39a2b37b24bc2159ee3bbd956331f2ace6855ee0b151c92fe2` |
| network.test | `1449823e90b538190b397ff474cbf3d9e8cb8fdb3ea7b808240c6b3b6bd4e187` |

Final namespace/table listings were empty and container-local forwarding values
were unchanged. The final matching container inventory was empty. Logs, scripts,
checksums, native verdicts and environment observations are retained locally in
`/private/tmp/hostel-network-status-simple-20261109/` and
`/private/tmp/hostel-network-status-simple-followup-20261109/`, with remote copies
on the run-owned data-disk paths.

This proves Hostel single-machine behavior under these conditions, not customer
kernel compatibility or Hibot → sandctl → Hostel business E2E. Shared browser and
daemon egress are outside the Bed-process policy scope.
