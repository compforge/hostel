# Independent Bed identity with shared software groups

Validated uncommitted Hostel worktree `bedspec-subsystem-requirements`, based on
`5385e0c0d2072515d23fc66c9497942c00241c83`, VERSION 0.2.10. This round builds on the
UID egress work and removes fixed UID/GID configuration, adding HOSTEL_BED_EXTRA_GIDS.
The Bedbox worktree `bed-extra-gids` is based on master `7bcb16b`; its 0.1.6 release
entry is a draft. The Hostel gitlink still requires the eventual upstream commit.
No production image was built/published and no customer environment was modified.

## Environment and artifacts

- Linux amd64 devbox, kernel 5.15.120.bsk.3-amd64.
- Disposable validation image derived from Bedbox 0.1.3, with ACL utilities,
  the actual Bedbox `build/prepare-shared-software.sh`, EXTRA_GIDS=1000 and the
  current Hostel test binary. This is not a full build of the Bedbox master Dockerfile.
- Final image ID: `sha256:2fec9aa0648ee258b8a62a4705038352a54d40776adceafc6cc4f36343572707`.
- Shared-network API tests: root daemon, default Docker capabilities plus NET_ADMIN,
  container-local ip_forward=0, test process and API target inside the same container.
- Private-files API tests: separate privileged container, ip_forward=1, required
  bwrap and shared network; command, session and Pillow main process exercised.
- Shared-software API test: existing image-target fixture starts and removes its
  own container, using host networking with default capabilities (no NET_ADMIN).
  Two Beds install/use/reinstall/uninstall cowsay 6.1 (pip and uv) and is-number 7.0.0
  (npm). Distinct actual UIDs and software group 1000 are asserted before mutations.
- All run-owned containers were removed; final filtered container inventory was empty.

| Artifact | SHA-256 |
| --- | --- |
| hostel-e2e | e7016778191d557b14a88369ddb832182d05513761bab700587187dc613c8011 |
| e2e.test | b9f5dc965d22b70071be0b5c64269f8d5a6375a7d9d9d69e2b047e74a595d755 |
| privilege.test | c115860a6634e226e317115f1c5105c2bc64a42cc0d673a80a99417ff98ca124 |

## Results

| Boundary | Result |
| --- | --- |
| Hostel root + protocol format/lint/full race tests, repocli validate | PASS, executed |
| Bedbox root build assets format/lint/test, repocli validate | PASS, executed |
| Linux privilege package, actual credential switching, access and missing-group rejection | PASS |
| TestBedExtraGroups local/supervisor | PASS, 0.43s / 0.49s |
| TestSharedUIDNetworkPolicyTraffic local/supervisor, with extra group 1000 | PASS, 6.53s / 6.69s |
| TestCarrierForwardingDisabled local/supervisor/required | PASS; UID policy explicitly disabled for the unenforced case |
| TestFeaturePoliciesOff | PASS |
| TestPrivateFilesWithSharedNetwork network-off/probe-failure | PASS, 1.10s / 1.09s |
| TestSharedSoftwareAcrossBedUsers pip/npm/uv | PASS, 2.68s / 4.00s / 1.21s |

Bedbox validation also discovered two components without format targets; those
format checks are unavailable, not passes. The changed build assets have a root
format/lint/test entrypoint and passed all three. No extension business code changed.

The public tests use the existing `make e2e` suite, compiled as documented for the
Linux container runner. Selections and runtime gates:

```sh
HOSTEL_E2E_BINARY=/usr/local/lib/hostel-e2e/hostel-e2e \
HOSTEL_E2E_REQUIRE_UID_POLICY=1 HOSTEL_E2E_REQUIRE_FORWARDING_DISABLED=1 \
  /usr/local/lib/hostel-e2e/e2e.test -test.run \
  '^(TestBedExtraGroups|TestSharedUIDNetworkPolicyTraffic|TestCarrierForwardingDisabled|TestFeaturePoliciesOff)$' \
  -test.v -test.timeout 5m

HOSTEL_E2E_BINARY=/usr/local/lib/hostel-e2e/hostel-e2e HOSTEL_E2E_REQUIRE_ROOTFUL=1 \
  /usr/local/lib/hostel-e2e/e2e.test -test.run '^TestPrivateFilesWithSharedNetwork$' \
  -test.v -test.timeout 5m

HOSTEL_E2E_IMAGE=hostel-extra-gids:validation HOSTEL_E2E_USERLAND=1 \
  /tmp/hostel-extra-gids-20261109/e2e.test \
  -test.run '^TestSharedSoftwareAcrossBedUsers$' -test.v -test.timeout 10m
```

Additional mechanism smoke: a disposable seccomp-unconfined container runs
setpriv UID/GID 200001 with group 1000, then bwrap --unshare-user with a writable
/usr/local bind. Group 1000 displays as unmapped 65534 inside that namespace, but
creating/stat-ing/removing a file in the software tree succeeds. This verifies
access under that mapping; it is not a full Hostel user-namespace API test.

## Failed rounds and remaining scope

- A new native access fixture initially used t.TempDir's private parent, blocking
  traversal before the intended permission assertion. Using a run-owned traversable
  parent fixed the fixture; the test then proved shared-group access and private denial.
- Software E2E initially supplied a binary to the image-only userland suite and was
  rejected by its existing guard. The corrected run uses the image-target lifecycle.
- Protocol fixture gained an additive supplementary_gids field; round-trip and
  detached status snapshot tests pass.

Logs and validation JSON are retained in ignored `runs/extra-gids-20261109/`.
The run does not establish compatibility on customer kernels 4.19/5.10, all package
manager chmod/move behaviors, concurrent installation safety, shared daemon/browser
egress, or a full hictld business workflow. Shared writable software remains a
Carrier-wide trust domain; independent UIDs do not make installed code private.
