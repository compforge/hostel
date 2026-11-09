# Network allocation reconciliation after daemon crash

Hostel 0.2.11, uncommitted worktree `bed-identity-e2e`, based on main
`1a4cd7fcf8f3bbed2267cdccafdbb5869e3e157c`. This implements recovery of Network
allocations within the existing subsystem lifecycle; Privilege continues to own
UID reservation and Forget. The original 0.2.10 failure and private-netns leak
are retained in [the baseline report](2026-11-09-identity-recovery.md).

## Implementation and scope

- Bed Manager binds `<beds-root>/.network` before Network Start. Ownership records
  are outside BedFS and portable Store data. Bed ID is the opaque owner key;
  exact random allocation names identify each table pair/netns/veth generation.
- Record intent atomically before the first kernel mutation, including namespace
  probes. Reconcile records before new allocations, even without a surviving Bed
  directory. Keep records after failed cleanup; remove only after cleanup succeeds.
- Hold a nonblocking filesystem lock for the lifetime of the Network subsystem.
  Never sweep other instances' tables by name prefix. Carrier netns and boot ID
  scope recovery; a changed carrier discards obsolete records without mutating
  the new namespace.
- Retain enforcement and refuse startup while an old UID/netns has live processes.
  Existing Executor parent-death handling terminates normal old execution trees;
  surviving processes require quiescence before recovery can proceed.
- Normal Release and crash recovery remove exact resources idempotently. UID is
  still forgotten only after all subsystem resources and local data are removed.
- Failure logs include owner, allocation and UID. Existing public APIs are unchanged.
  VERSION increments from 0.2.10 to 0.2.11.

## Environment and entrypoints

Linux amd64 devbox, kernel `5.15.120.bsk.3-amd64`. Disposable userland image:
`hostel-extra-gids:validation`, ID
`sha256:2fec9aa0648ee258b8a62a4705038352a54d40776adceafc6cc4f36343572707`.
This reuses the Bedbox 0.1.3-based validation overlay; it is not a full Bedbox
0.1.6 production image. Freshly compiled Hostel and test binaries are bind-mounted.

The existing Makefile E2E suite was cross-compiled using the documented colocated
Linux runner pattern. The run script uses separate disposable containers:

- Shared UID profile: default Docker capabilities plus NET_ADMIN, ip_forward=0,
  Seccomp=2. Public APIs use run-owned loopback ports inside the carrier.
- Private netns profile: privileged, ip_forward=1, Seccomp=0.
- Kernel UID traffic regression: NET_ADMIN, ip_forward=0.
- Kernel private-network regression: privileged, ip_forward=1.

`HOSTEL_E2E_ENVIRONMENT_FILE` points to the existing devbox environment config;
`HOSTEL_E2E_ON_TARGET=1` records colocated execution. Carrier observations in the
adapter's host field do not claim an independent host-OS probe.

Native checks use `repocli validate --base HEAD --check format,lint,test`:
root Makefile lint and affected race tests passed, `complete=true`,
`inputsConsistent=true`. Linux-specific tests below are executed separately,
not inferred from macOS compilation or skipped platform gates.

## Final selections

| Selection | Required behavior |
| --- | --- |
| `TestBedIdentityCrashRecovery` | Retained UID/Bed identity, data ownership and extra group; reserve before newcomer; recovered deny-to-allow works; purge/recreate completes; no allocation JSON records after fixture close |
| `TestPrivateNetworkCrashRecovery` | New netns with retained Bed ID/UID; policy updates work; old named netns/Carrier tables and JSON records all gone; unrelated sentinel nft table preserved |
| `TestBedUIDPillowEgress` | Local/supervisor main-process policy, identity/groups and policy reversal after restart |
| `TestBedExtraGroups` | Local/supervisor command/session credential regression |
| `TestSharedUIDNetworkPolicyTraffic` | Local/supervisor live traffic, sessions and recreation |
| `TestCarrierForwardingDisabled`, `TestFeaturePoliciesOff` | Disabled-mechanism and best-effort/required behavior |
| `TestNetworkPolicyTraffic` | Local/supervisor independent-netns traffic regression |
| `TestAllocationStoreRecoveryRetainsFailedCleanup` | Concurrent owner refused, cleanup failure preserves record, retry succeeds, closed store cannot allocate |
| `TestAllocationStoreCarrierChangeAndMalformedRecord` | Changed carrier avoids current kernel mutations; malformed ownership blocks recovery and is retained |
| `TestAllocationStoreRetainsEnforcementForLiveUID` | Actual child UID prevents nft mutation; after process exit the same record reconciles |
| `TestLinuxNetworkTableCleanup` | Atomic filter/NAT cleanup failure and retry |
| `TestLinuxPolicySeparatesNetworksAndRevokesAccess` | Private-network separation/revocation regression |
| `TestUIDPolicyTraffic` | Actual UID nft/DNS traffic and revocation regression |

Runtime flags and exact commands are retained in `run-recovery.sh`. The initial
combined kernel run incorrectly selected UID traffic with ip_forward=1; that case
failed its prerequisite assertion, while store/private cases passed. Final runs
separate forwarding conditions. This environment error is retained as
`kernel-fixed.log`, not counted as a product failure or a pass.

Earlier successful runs used builds preceding the final idempotent-close and
explicit-owner API refinements. Final binary hashes and source-matched rerun
results are retained separately below; earlier passes are not substituted for them.

## Evidence and limitations

Ignored evidence: `runs/network-reconciliation-20261109/`. It retains
`validation.json`, final script, hashes, native logs and environment verdicts,
plus earlier runs for provenance. No host sysctl/firewall or shared application
container was changed. All fixtures own their processes and disposable containers;
final cleanup inventory is retained alongside the logs. Images, build context
and evidence remain for follow-up.

Records are atomic against process crashes, without fsync/power-loss durability.
Unjournaled resources from older versions cannot safely be attributed: no automatic
prefix-based cleanup is attempted. A prior normal shutdown or Carrier replacement
is needed for such legacy orphans. Customer kernels, unprivileged userns and the
actual hictld/HiBot business chain are outside this run.

No image push, deployment, dependency-pin update, commit or PR was performed.

## Final observed results

All final selections passed with no skips. Shared API run:
`20261109T072904.303250302Z`; private API run:
`20261109T072923.489979056Z`. Both include full fixture cleanup before the native
verdict is written. Private recovery reports an empty named-netns and nft table
inventory after sentinel removal. The shared recovery case reaches purge and
same-name recreation, and both cases assert empty allocation-record inventory.

Kernel results: three allocation-store cases passed, including the live child
UID; filter/NAT cleanup retry passed (0.23s); private separation/revocation passed
(2.37s); UID traffic passed with forwarding disabled (5.55s).

| Final artifact | SHA-256 |
| --- | --- |
| hostel-e2e | fe948060c5ec1e875b27fb3caddd2acf7a87ca985abaea9c1bcc29f350917474 |
| e2e.test | 452e4d6e5aa6a2802ddfd100aaa403126506b5baedefe9eaac49cecf41f7288e |
| network.test | 47a59f8eb8d4ce64a079b7cf2489ef039a8b084638d022f31d67031b56fb3054 |

Native lint/race report remains complete/passed with consistent inputs. See
`cleanup-final.txt` for the final empty inventory of both run-owned container
prefixes after evidence permission repair and collection.
