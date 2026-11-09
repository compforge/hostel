# Post-171 identity and UID egress lifecycle E2E

PR #171 was confirmed merged. The target is Hostel main
`1a4cd7fcf8f3bbed2267cdccafdbb5869e3e157c`, VERSION 0.2.10. The test process adds
uncommitted E2E coverage in worktree `bed-identity-e2e`; runtime code is unchanged.
This run found a real recovery failure. It is not a release-readiness pass.

## Target and execution conditions

- Executed 2026-11-09, approximately 07:09–07:12 UTC, on amd devbox.
- Kernel `5.15.120.bsk.3-amd64`; Debian 12 container userland, amd64,
  Seccomp=2 and default Docker capabilities plus NET_ADMIN.
- Container-local `net.ipv4.ip_forward=0`. Test process and newly compiled Hostel
  binary share the disposable carrier. Each fixture uses its own loopback HTTP
  port, checks `/healthz`, then exercises public Bed APIs and real TCP traffic.
- Reused validation userland image `hostel-extra-gids:validation`, ID
  `sha256:2fec9aa0648ee258b8a62a4705038352a54d40776adceafc6cc4f36343572707`.
  It derives from Bedbox 0.1.3 with shared-software ACL setup. The tested Hostel
  binary is supplied by a bind mount; this is not a full Bedbox 0.1.6 image build.
- New cases explicitly require UID file confinement and UID+nft, disable netns
  and filesystem view helpers, and request exactly extra GID 1000.
- Native environment adapter records carrier observations. Its `host` facts
  come from inside this colocated container, not an independent host-OS probe.

| Executed artifact | SHA-256 |
| --- | --- |
| hostel-e2e | 19fd96d43f0d70cf5f0f612fba204552881e8063dcfb9c0cf84f115be872781a |
| e2e.test | 49625019209df3b4554d730e0a0e5f92bb75fa330f477ff938cabc1c90b174f0 |

## Cases and verdicts

| Case | Native result | Evidence |
| --- | --- | --- |
| New `TestBedUIDPillowEgress` | PASS, local and supervisor | Both initial allow/deny and reversed policy after Pillow restart; distinct UIDs, exact extra group and process UID match status |
| New `TestBedIdentityCrashRecovery` | FAIL after crash | Bed ID/UID, extra group, file owner/data and neighbor denial pass; deny-to-allow update cannot restore connectivity |
| `TestBedExtraGroups` | PASS, local and supervisor | Existing command/session credential regression |
| `TestSharedUIDNetworkPolicyTraffic` | PASS, local and supervisor | Existing shared-network traffic and live policy regression |
| `TestCarrierForwardingDisabled` | PASS, local/supervisor/required | Existing no-UID-policy fallback and required-feature failure |
| `TestFeaturePoliciesOff` | PASS | Explicit mechanism-disable behavior |

The passing regression selection completed in about 21 seconds, without skips.
The separate diagnostic recovery run failed in 4.62 seconds. It stops at the
failed allowed-connection assertion, so its later purge/recreation assertions
were **not reached**. Regular recreation is covered by the existing traffic case.

## Recovery failure

The fixture kills only its own daemon and waits for process exit. A replacement
starts on the retained beds-root, admits a newcomer first, then redeclares the
retained Bed and its deny policy. Identity reservation and private data survive.
The newcomer reaches the TCP fixture; the resumed Bed correctly fails under deny.
After the API accepts an allow policy and the daemon logs `mode=allow_all`, the
same Bed still times out:

```text
Bed bed-2fb63a804677312c8fdcad4c59cf2c89 acquired UID 221420
allowed connection failed: dial tcp4 127.0.0.1:36737: i/o timeout
```

After replacement-daemon cleanup, `nft list ruleset` still contains the old
`inet hu_bd1427b1d02d11e3` table, with `meta skuid 221420 jump workload` and a
terminal drop. Its `ip` table also retains DNS redirection to the dead daemon's
port 45842. The control Bed's old tables remain too.

`internal/host/network/uid_policy_linux.go` allocates random table names and keeps
leases only in process memory. A fresh backend neither recovers nor removes the
previous daemon's leases. New allow rules cannot override an old base-chain drop.
The observed defect is daemon restart **inside a surviving network namespace**;
this does not show the same failure after replacing the entire carrier netns.
Recovery needs durable/scoped ownership, safe reconciliation of prior allocations
and their DNS rules, and protection against deleting another live daemon's rules.
No runtime fix is included in this test-only change.

## Entrypoint and retained evidence

The existing `make e2e` source suite was cross-compiled using its documented Linux
container-runner pattern (`go build -tags=e2e` and `go test -tags=e2e -c`). Runs use:

```sh
docker run --rm --name <run-owned-name> --cap-add NET_ADMIN \
  --sysctl net.ipv4.ip_forward=0 \
  -v /tmp/hostel-identity-e2e-20261109:/usr/local/lib/hostel-e2e \
  --entrypoint /bin/sh hostel-extra-gids:validation -c '
    HOSTEL_E2E_SSH_HOST=10.37.94.68 \
    HOSTEL_E2E_BINARY=/usr/local/lib/hostel-e2e/hostel-e2e \
    HOSTEL_E2E_REQUIRE_UID_POLICY=1 HOSTEL_E2E_REQUIRE_FORWARDING_DISABLED=1 \
    HOSTEL_E2E_ON_TARGET=1 \
    HOSTEL_E2E_ENVIRONMENT_FILE=/usr/local/lib/hostel-e2e/environment.yaml \
    HOSTEL_E2E_RUNS_DIR=/usr/local/lib/hostel-e2e/runs \
    HOSTEL_E2E_REVISION=1a4cd7f+identity-e2e \
    /usr/local/lib/hostel-e2e/e2e.test -test.run "<selector>" -test.v -test.timeout 5m'
```

Exact selections:

- Failure diagnostic: `^TestBedIdentityCrashRecovery$` (timeout 2m).
- Regression: `^(TestBedUIDPillowEgress|TestBedExtraGroups|TestSharedUIDNetworkPolicyTraffic|TestCarrierForwardingDisabled|TestFeaturePoliciesOff)$`.

Ignored local evidence is retained in `runs/identity-recovery-20261109/`:

- `hostel-identity-e2e-diagnostic.log`, including original command error and residual rules.
- `hostel-identity-e2e-regression.log`.
- `hostel-runtime/20261109T071146.522923561Z/`: failed diagnostic verdict/environment.
- `hostel-runtime/20261109T071214.577238475Z/`: passed regression verdict/environment.
- Earlier attempts: environment error from missing `HOSTEL_E2E_SSH_HOST`; first
  test run incorrectly expected HTTP 200 for asynchronous restart (corrected to
  202); a second run localized the real recovery failure. These are retained and
  are not counted as passes.
- `validation.json`: repocli format/lint/affected tests complete and passed,
  `inputsConsistent=true`. Native macOS tests do not prove Linux-gated E2E; the
  devbox executions above provide that evidence.
- `cleanup.txt`: no run-owned `hostel-identity-e2e*` containers remained at
  07:12:37 UTC. The subsequent artifact-permission helper also used `--rm`.

Container removal deletes the run-owned network namespace and its stale rules.
No host firewall/sysctl or shared application container was changed. Build context,
image and evidence are retained for follow-up. No image was pushed or deployed.
Customer kernels, unprivileged userns, and actual hictld/HiBot business integration
remain unverified by this round.

## Independent-netns comparison

A follow-up `TestPrivateNetworkCrashRecovery` executed at 07:15:26–07:15:29 UTC
in another disposable container, same runtime binary/image, with `--privileged`
and container-local `ip_forward=1`. It requires netns and UID file confinement;
UID+nft is disabled. Seccomp=0 for this privilege profile. Its runner SHA-256 is
`d3de1003a6b281fe6b75ebbe5650d01b90833dc251747c3049d6ed57960fa2b6`.

The connectivity assertions **passed** in 3.67s:

- Retained Bed `bed-54961e8d25a6e0d2f15b764edf42eb7f` kept UID 205624.
- Its netns changed `net:[4026533424]` → `net:[4026533501]` and gateway changed
  `198.18.0.1` → `198.18.0.5`.
- Deny still blocked after recovery; updating to allow restored real connectivity.
- After both daemon fixtures closed, namespace `hndb5654af38ad` and Carrier
  `inet`/`ip` tables of that name remained. Resource collection is diagnostic,
  not a passing cleanup assertion. The case passes connectivity recovery only.

This confirms that independent netns avoids the observed old-UID-rule interference
when recovery creates a fresh namespace, but does **not** provide crash cleanup by
itself. The current private backend also tracks resource ownership only in memory.
Named netns, veth/routes, resolver configuration and Carrier tables need recovery
reconciliation. Carrier removal remains the cleanup boundary for this experiment.

Evidence: `hostel-identity-e2e-netns.log` and
`hostel-runtime/20261109T071529.748148853Z/`. Reproduce with the command above,
substituting `--privileged --sysctl net.ipv4.ip_forward=1`, setting
`HOSTEL_E2E_REQUIRE_NETWORK=1` instead of the UID/forwarding gates, and selecting
`^TestPrivateNetworkCrashRecovery$` (timeout 3m). `cleanup-final.txt` records the
final empty run-container inventory after evidence collection.

UID allocation is keyed by internal Bed ID, not caller-owned Bed name. Retained
local recovery and Executor/Pillow replacement preserve UID. Deletion/Forget
followed by same-name creation obtains a new Bed ID and a new allocation; the
finite UID pool permits numeric reuse, so a different number is not guaranteed.
