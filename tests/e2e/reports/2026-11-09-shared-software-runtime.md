# Shared software prepared by the Filesystem instance lifecycle

Hostel 0.2.14, based on main `b1db6eca474b0784e1e67751eaddd6c910ff3d0b`.
The Filesystem subsystem prepares explicitly authorized Carrier software directories
in Start, before admitting Beds. Privilege still owns identities and supplementary
groups. Bed teardown and instance Close do not delete shared software or undo its
metadata. The permission and failure contracts are in [isolation](../../../docs/isolation.md).

## Initial artifact-backed run

Disposable, exported Docker image on the amd64 devbox; Debian 12 userland,
kernel `5.15.120.bsk.3-amd64`. Base is the published Bedbox 0.1.7 index
`sha256:e35d94aa6e0b953659723759b34c4c84281d2d8bf04476bdd8dca6aee089fe60`,
with the final Hostel binary and project-owned test runners copied in. This is
an overlay validation image, not a full build of a new Bedbox release.

The base's `/usr/local` had no default ACL after image export. Configuration:
`HOSTEL_SHARED_SOFTWARE=[{"path":"/usr/local","gid":1000,"policy":"required"}]`
and `HOSTEL_BED_EXTRA_GIDS=1000`. Actual API E2E used Docker default capabilities
plus NET_ADMIN and SYS_PTRACE, seccomp/AppArmor unconfined, and
`net.ipv4.ip_forward=0`. Observed CapEff/CapBnd: `00000000a80c35fb`.
No host sysctl or unrelated workload was changed.

| Artifact | SHA-256 |
| --- | --- |
| amd64 overlay image | `18e70f9ec29a7700fe6d3170f61951a9b86c3e46366a2174ad913bd6b780c6e6` |
| amd64 Hostel binary | `1e250e21785b1a6a1acf83d1a7093038f9eeff421eddbbdf923b003f3f07d8ef` |
| amd64 E2E runner | `38681ebc133e50038271d174a4a0116f0d0b42aa4ce3dd26e6bd61a35750e4b2` |
| amd64 host filesystem runner | `645ba04dd3785d33b5f04dd8c539f3915a1468873a45aefd0decea5c57debb3b` |
| arm64 overlay image | `24de3dd0e120d42ada9a13a2f92d8d07a112cfbe8e29b21ce4c61f40fa254b4c` |

## Initial results

Native macOS `repocli validate --base HEAD --check format,lint,test` passed:
root Makefile lint and affected tests, complete=true, inputsConsistent=true.
Both Linux daemon architectures compile. Linux-specific behavior was executed,
not inferred from macOS skips:

- `TestPrepareSharedSoftwareRuntime`: group write, inherited group/default ACL,
  preserved UID, idempotence, external symlink unchanged, symlink root rejected,
  multiply-linked file rejected without changing its outside inode.
- `TestSharedSoftwareRequiresActualGroupMembership`: UID 200001 creates with
  umask 022; UID 200002 with GID 1000 appends; UID 200003 without that group is denied.
- `TestSharedSoftwarePreservesNamedDefaultACL`: existing named user grant survives.
- Filesystem lifecycle tests: once per instance, Close retains data, auto/off/required
  failures and immutable status snapshot.

These kernel/lifecycle tests passed on native amd64 and arm64 under QEMU on the
same amd64 host. QEMU is not evidence of a separate arm64 kernel or deployment.

The initial native amd64 public API E2E selected
`^TestSharedSoftware(RuntimePermissions|AcrossBedUsers)$`, no skips:

- Two distinct Bed UIDs, ordinary nested file create/append/rename/replace/delete:
  passed (1.57 seconds). Status confirms runtime shared-software preparation.
- Cross-Bed pip, npm and uv install/use/replace/uninstall: passed (7.25 seconds).
  Package subcases took 2.28/2.18/1.00 seconds respectively.
- Suite verdict: `20261109T120120.010009855Z`, status pass, 8.825 seconds.

Supplementary-group assertions use the public Carrier-space assigned credential
report. A private user namespace can display an unmapped extra group as overflow
GID in `/proc`; real cross-UID software access is verified independently.

## Evidence and limits

Ignored local evidence: `runs/shared-software-runtime-20261109/`; original remote
logs/context: `~/.cache/hostel-shared-runtime-20261109/` on the amd64 devbox.
Final logs use `*-final*` and arm kernel tests use `*-arm*`; earlier attempts are
retained separately. Initial binary invocation failed the previous image-only
userland guard; an initial image lacked NET_ADMIN for an unrelated network probe;
a later run failed the old in-namespace numeric-GID assertion. Those failures are
not substituted for the final passes. All run-owned containers were removed.

Explicit 0600 creations or later chmod can revoke shared group write; ACLs do not
override an application's explicit mode. Private Bed data is unchanged. Named
ACLs are preserved; symlinks are skipped, and hardlinked or privileged regular
files fail preparation. Partial preparation is reported and is not rolled back.
Custom paths also need the existing Pillow Volume/Mount view; the new setting
only authorizes Carrier metadata preparation.

This run does not verify a new complete Bedbox Dockerfile, a registry round trip,
customer kernels, native ARM execution, or the sandctl/hictld/HiBot business chain.
The existing Network API adaptation requirement remains separate from this change.

## Access ACL and disposable-runner follow-up

A final review found that chmod updates the mask of an existing extended access
ACL without necessarily granting its owning group write. Preparation now updates
that entry and mask as well, retaining named grants. The Linux test
`TestSharedSoftwareUpdatesExtendedAccessACL` creates a read-only owning-group
entry plus a named user grant, then verifies a real supplementary-GID-1000 child
can append after preparation. All four host filesystem cases passed on native
amd64 and arm64 under QEMU; native format/lint/affected tests also passed with
complete, consistent inputs.

The direct binary userland route additionally requires
`HOSTEL_E2E_DISPOSABLE=1`. A negative invocation without this flag fails before
starting a Bed or installing packages; the explicit disposable invocation passes.
This addresses the local automated review finding. That original review was
incomplete for the host filesystem unit because its LLM stream terminated;
it is not reported as a clean review.

Final follow-up API run: `20261109T121647.731022288Z`, pass with no skips.
The same exported amd64 overlay image was used, with the final daemon mounted
read-only at `/usr/bin/hostel`; this follow-up is binary validation within an
exported image, not a new final-image build. Cross-Bed pip/npm/uv took 7.66 seconds
(2.35/2.19/0.97); ordinary cross-UID file operations took 1.69 seconds. The same
ip_forward=0 and capability profile applies.

| Follow-up artifact | SHA-256 |
| --- | --- |
| amd64 daemon | `b7d33e6e3e50af2b79341763d0ab7cb4f279c7743df389db57378028f281285c` |
| arm64 daemon | `298843c706cd389719c26278fcb08c0bbed1c11c1c73c65fe5745f3aef11dd93` |
| final amd64 API runner | `abce2424feefedd1f06e9634b123966b97946ff9d6e36f2d2de69679cda76384` |
| amd64 host filesystem runner | `5890a7a660c24c34846f85ca2da4b5e126783da957e734d486e6e6f803f8584d` |
| arm64 host filesystem runner | `844cbe1291f389aeb5a1dc7236be62c8d1d14fbba8e387377166a02463cc2b26` |

Earlier complete Bedbox master-image registry validation used the preceding
runtime revision, and remains separate evidence. Its initial smoke used the
unsupported `hostel version` subcommand, then a wrong hictld PATH assumption;
the corrected version/path smoke and public API suite passed. Subsequent full
Bedbox builds must pin the access-ACL follow-up before release.


## Multiuser filesystem follow-up after PR176

This follow-up tests the uncommitted `feat/fs-multiuser` tree based on main
`aefcefe390ad5b4fb51f4eac1d24c6d6b4ae2990`, with VERSION 0.2.15. It supersedes the
configurable group/directory setup described in the earlier rounds above:
Privilege selects internal software GID 199999 automatically, and Filesystem
prepares the internal software area. No Bed extra-GID or shared-directory env
configuration is needed. File API operations now execute under the Bed identity.

The amd64 devbox ran the existing compiled E2E suite in disposable containers
using cached image `hostel-shared-runtime:validation`, with the changed binary
mounted at `/usr/lib/hostel-e2e`. The image itself was not rebuilt or published.
Observed platform: Debian 12, Linux 5.15.120.bsk.3-amd64. The NET_ADMIN profile
retained seccomp mode 2; the privileged mount profile reported seccomp mode 0.

| Verification | Result |
| --- | --- |
| Native format/lint and impacted race tests through repocli | Passed; root lint ran in full |
| Linux amd64 and arm64 daemon builds | Passed; arm64 was build-only |
| Linux file worker and BedFS identity/confinement tests | Passed, including root-owned mutation denial and accessible out-of-root symlink rejection |
| Full Linux BedFS tests | Passed, including nested mount traversal and common-parent exclusion |
| Fresh image shared software preparation and API/process cross-UID writes | Passed; measured Carrier startup 65.208 seconds |
| Automatic software group, UID/PRoot and Carrier file API matrix | Passed for local and supervisor |
| UID+nft egress with ip_forward=0 | Passed for local and supervisor, including live policy changes and unchanged forwarding value |
| Privileged prepared mount view | Passed for command, Session and Pillow main; private identity and capability assertions passed |
| pip/npm/uv cross-UID install/use/replace/uninstall | Passed; package subcases 3.03/4.30/1.08 seconds; suite including cold startup 121.41 seconds |

Final suite verdicts, all pass:
`20261109T153925.997294786Z`, `20261109T153950.722588928Z`,
`20261109T154145.540956125Z`, `20261109T154238.145578175Z`.
Raw logs, verdicts and observed environments are retained in ignored
`runs/fs-multiuser-20261109/`; remote originals are under
`/data00/tmp/hostel-fs-multiuser-0.2.15/`. Test containers were removed.

Artifact SHA256:

- Daemon: `abf06ea239d047c82a11d35633c94ddee9c1103e7de5641b99d4ddbb700bcb4a`
- E2E runner: `4dc5596d668065e54db3e28a63119ae58f89de11480eba2975a667b339d32ade`

Earlier failures remain in the evidence: default Docker capabilities exposed an
existing nft probe-cleanup failure without NET_ADMIN; a cold image exceeded the
old 45-second fixture wait; a network probe executable under `/e2e` was outside
the PRoot view. The final runs provide NET_ADMIN, allow 150 seconds for the
existing two-minute preparation budget plus startup probes, and place the runner
in the read-only runtime tree. Production preparation timeout was not increased.

Cold preparation of an old image has material copy-up cost; these passes do not
establish an acceptable startup budget for every image or storage backend.
Explicit file modes/chmod can still revoke shared group access, and shared
software remains a common trust domain. This run does not prove customer-kernel
compatibility, concurrent package-manager safety, a complete Bedbox image release,
or the sandctl/hictld/HiBot business chain.


## FS realization and subsystem best effort, 2026-11-10

This round validates the uncommitted `worktree-fs-realization` tree based on
PR177 merge `c58143d7fc153a43c5233bf1cfb92e331ccd9142`, VERSION 0.2.16.
The public path policy is shared by BedFS and process mechanisms. Per-Bed FS
realization now distinguishes preparation, private-data protection, core access,
process views and shared-software authorization. Existing Subsystem.Status()
aggregation and domain-owned Bed status writers remain the owners.

Native format, full lint and impacted race tests passed through
`repocli validate --repo . --base HEAD --check format,lint,test --timeout 10m`.
Linux amd64 and arm64 daemon builds passed; arm64 was build-only. Linux host
filesystem and Bed filesystem package tests passed, including failed preparation,
partial release/retry, snapshot detachment and the nonzero Landlock ABI floor.
The common mount-policy and Landlock path-grant tests also passed. These Landlock
checks do not establish actual kernel enforcement on this target.

Live E2E used the project-owned compiled runner in sequential disposable amd64
containers on devbox, cached `hostel-shared-runtime:validation`, Debian 12 and
kernel `5.15.120.bsk.3-amd64`. Daemon and runner were mounted read-only under
`/usr/lib/hostel-e2e`; `tests/e2e/environments/devbox/config.yaml` selected the
colocated binary target with `HOSTEL_E2E_ON_TARGET=1` and
`HOSTEL_E2E_DISPOSABLE=1`. No business environment or image tag was changed.

| Profile and cases | Observed result |
| --- | --- |
| NET_ADMIN, network none, ip_forward=0: TestSharedSoftwareRuntimePermissions | Pass; software prepared and authorized for both UIDs, real cross-UID API/process writes; cold startup 34.683 seconds |
| Same profile: TestBedIdentityCrashRecovery, TestBedUIDPillowEgress, TestSharedUIDNetworkPolicyTraffic | Pass; retained identity, Pillow main and live policy changes; local/supervisor where provided |
| Same profile: TestCarrierForwardingDisabled, TestFeaturePoliciesOff, TestFilesystemPermissions | Pass; unmet policy disclosed, required rejected, forwarding unchanged; UID+PRoot and Carrier views match Bed realization |
| Privileged, network none, ip_forward=1: TestFilesystemPermissions, TestRootfulPreparedMount | Pass; bwrap/mount rootfs, RW/RO contracts and command/Session/Pillow identity |
| Same privileged profile: TestPrivateNetworkCrashRecovery | Pass; retained UID, new namespace and restored/updated policy; no remaining netns/nft tables after fixture cleanup |
| Same privileged profile: TestPrivateFilesWithSharedNetwork | Initial network-probe-failure fixture failed; corrected fixture passed both subcases, with actual FS protection and honestly unenforced egress |

The initial failing fixture returned failure for every nft call, including
cleanup. Startup correctly refused to abandon pending resource ownership.
The fixture now rejects table/rule installation while delegating cleanup to the
real nft executable. Only that case was rerun after the fixture correction;
production cleanup semantics and Network implementation were not weakened.
The earlier failure and its logs remain in the evidence. No selected cases skipped.

Runner selections (each uses `-test.v`; software/fallback timeout 4m, other groups 6m):

```text
^TestSharedSoftwareRuntimePermissions$
^(TestFilesystemPermissions|TestSharedUIDNetworkPolicyTraffic|TestBedIdentityCrashRecovery|TestBedUIDPillowEgress|TestCarrierForwardingDisabled|TestFeaturePoliciesOff)$
^(TestRootfulPreparedMount|TestFilesystemPermissions|TestPrivateFilesWithSharedNetwork|TestPrivateNetworkCrashRecovery)$
^TestPrivateFilesWithSharedNetwork$
```

The UID group sets REQUIRE_UID_POLICY and REQUIRE_FORWARDING_DISABLED;
software sets USERLAND; mount sets REQUIRE_ROOTFUL and REQUIRE_NETWORK;
the corrected fallback run sets REQUIRE_ROOTFUL. All names have the
`HOSTEL_E2E_` prefix. The NET_ADMIN profile retained seccomp mode 2; privileged
runs used seccomp mode 0. API readiness and the test operations, rather than the
cached image's unrelated Docker healthcheck, establish the test outcomes.

Run verdicts:

- Software: `20261110T020548.994147959Z`, pass.
- UID/forwarding: `20261110T020629.005735654Z`, pass.
- Mount/recovery/old fallback fixture: `20261110T021039.539138736Z`, fail as explained above.
- Corrected fallback fixture: `20261110T021143.439864405Z`, pass.

Raw logs, verdicts, observed environments and final local validation output are
retained in ignored `runs/fs-realization-20261110/`; remote originals remain in
`/data00/tmp/hostel-fs-realization-0.2.16/results/`. All task containers were removed.
The first artifact copy could not read root-owned environment files; the read-only
copy through sudo rsync completed without changing result permissions.

Final mount/fallback daemon SHA256:
`46393e030e8ba7d4b5fc9dd17bb97d2add0e862005c1a0f970e401a90445a75c`.
The UID run preceded only a Landlock failure-message edit and an additional
preparation-failure unit case. Initial E2E runner SHA256:
`864b3a226cd390370676a4b1e26af614e2bad3ad0babfb07fc1d1280e647812f`;
corrected fixture runner:
`ef6685988c7234642dd20313d7868f99f8f3bad6af88bc9e2ca91abb678b06dc`.

This round does not revalidate real pip/npm/uv package installation, customer
kernels, native arm64, concurrent package-manager safety, or the sandctl/HiBot
business chain. Status is the last confirmed preparation/cleanup observation,
not continuous detection of external ACL changes or file-worker death. Cold
startup measurements remain observations, not a performance acceptance result.
