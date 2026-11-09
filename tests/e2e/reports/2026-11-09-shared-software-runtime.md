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
