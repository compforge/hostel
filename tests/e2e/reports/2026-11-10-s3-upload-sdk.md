# S3 upload SDK comparison — 2026-11-10

Status: the initial SDK comparison below used an explicit 8 MiB threshold.
The final implementation removes that override and uses SDK defaults; its
validation is recorded in the final section. Stable-source upload scenarios
passed on both SDK versions in the initial comparison.
The concurrent-truncation experiment below is outside the supported source
stability contract and is not a release gate. The final focused validation passed with the current dependencies.
This is S3 backend integration evidence, not daemon/business E2E or a deployment.

## Initial SDK comparison (before removing sizing overrides)

- Hostel base: `cef44690`, worktree `s3-managed-upload`.
- All uploads delegated to AWS transfermanager, original seekable source retained,
  `ContentLength` and `MpuObjectSize` set to the caller's declared size.
- Threshold and initial part size: 8 MiB; concurrency: 2; failure cleanup timeout: 30 s.
- Current dependencies remain unchanged. Latest-version comparison used an alternate
  `/tmp/hostel-s3-v056.mod` and `.sum`.

| Module | Current | Comparison |
| --- | --- | --- |
| transfermanager | v0.2.3 | v0.5.6 |
| service/s3 | v1.104.2 | v1.114.9 |
| config | v1.32.27 | v1.33.17 |
| credentials | v1.19.26 | v1.20.17 |
| smithy-go | v1.27.3 | v1.28.8 |

The official Go proxy confirmed v0.5.6, released 2026-11-09;
the configured company proxy still reported v0.5.5 as latest.
Upstream changelog includes concurrent-reader panic fixes, preservation of the
first failure, checksum configuration and directory-transfer fixes. Upgrade alone
does not enforce the caller's exact object length.

## Environment and results

Both versions ran the same compiled backend integration test on the AMD Linux
devbox against isolated MinIO `RELEASE.2025-04-22T22-12-26Z`, built from the official
tag. MinIO ran with temporary in-memory storage and a loopback-only endpoint;
the test's TLS bridge exercised HTTPS aws-chunked requests. The source build was
used because official image pulls and binary download were unavailable.

| Scenario | v0.2.3 | v0.5.6 |
| --- | --- | --- |
| Empty, below/equal/above 8 MiB, 32 MiB pack | 5 passed | 5 passed |
| Metadata and downloaded SHA-256 | passed | passed |
| Ordinary/multipart conditional nonoverwrite | 2 passed | 2 passed |
| Cancellation and upload cleanup | passed | passed |
| Concurrent truncation experiment: declared 17 MiB, actual 0/4/8/9 MiB | shorter objects published | shorter objects published |

All four truncation experiments returned success and published the shorter object.
The initial experimental assertions expected rejection, so those historical runs
exited with failure. Those assertions have been removed from the regression suite:
Hostel requires a stable upload source and does not promise to detect concurrent
mutation. Final eviction stops managed writers; persistence uploads finalized
buffers or archives; explicit Transfer callers own source stability. See
[the upload contract](../../../docs/volumes.md#pack-object-大小与-s3-上传分片).
The deterministic reproduction obtains the original file size, truncates the
file, then invokes upload using that previously observed size. This represents
another writer truncating a file between stat and upload; it does not imply
ordinary uploads of stable 0/4/8/9 MiB files fail.

The SDK ordinary-PUT mapping omits the supplied ContentLength. Multipart forwards
MpuObjectSize, but this MinIO release does not enforce it. A mock server that
rejects mismatched MpuObjectSize catches the 8/9 MiB cases and verifies abort;
0/4 MiB still publish through ordinary PUT. This is separate from snapshot
consistency under concurrent writes.

Both live runs verified zero remaining objects and zero unfinished uploads.
The test-owned bucket was removed and the temporary container stopped/removed.
The latest SDK logged `Skipped validation of multipart checksum`; independent
readback SHA-256 verification passed, which must not be reported as SDK checksum
validation passing.

## Validation and limits

- Final current-version validation (`repocli validate --repo . --base HEAD
  --check format,lint,test --timeout 10m`) passed: full format/lint and focused
  race tests across API, Manager, Store/backend/sync, Config and E2E packages,
  plus the protocol module tests. The external S3 integration test was skipped
  in this local run because its endpoint was unset; live MinIO evidence is above.
- Earlier current-version focused tests failed on the two ordinary PUT
  experimental rejection assertions, before removal of that unsupported contract.
- Latest-version Linux backend test compilation passed. Focused race checks
  likewise fail on those two experimental assertions; the other selected packages passed.
- Latest comparison root vet passed, but the nested protocol lint invocation
  failed because inherited GOFLAGS points at the root alternate modfile and its
  relative protocol replacement. This is a comparison-harness limitation, not
  evidence of an SDK compile error; the full lint gate is not passed.
- TOS was not run: no authorized writable test bucket/config supplied.
- No commit, PR or deployment is claimed. Exact-length enforcement under
  concurrent mutation is outside the Hostel contract, not an outstanding fix.

Local raw evidence:
`/tmp/hostel-s3-managed-first.log`, `/tmp/hostel-s3-managed-v056.log`,
`/tmp/hostel-s3-minio-v023.log`, `/tmp/hostel-s3-minio-v056.log`,
`/tmp/hostel-s3-v056-resolve.log`,
`/tmp/hostel-s3-stable-source-validation.log`.

## Final SDK-default configuration

The final change removes Hostel's S3 part-size flag, environment setting,
Store/Options fields, backend sizing field and validation. Upload selection and
part sizing use transfermanager v0.2.3 defaults: a 16 MiB multipart threshold and
8 MiB initial parts. Pack target size remains a persistence-layout setting.
Concurrency (2) and failed-upload cleanup timeout (30 s) retain their resource
budgets. Dependencies were not upgraded.

`repocli validate --repo . --base HEAD --check format,lint,test --timeout 10m`
passed full format/lint and focused race tests, including protocol checks.
The final Linux backend test binary passed all nine MinIO subtests:

| Object size / scenario | Observed result |
| --- | --- |
| 0, 8 MiB, 16 MiB minus 1 byte | ordinary PUT, matching SHA-256 and metadata |
| 16 MiB | multipart, 2 parts |
| 16 MiB plus 1 byte | multipart, 3 parts |
| 32 MiB pack | multipart, 4 parts |
| 4 MiB / 17 MiB no-overwrite | conflict, original object preserved |
| cancellation | failed upload, no object committed, multipart cleaned |

The ordinary PUT below threshold used a 16 MiB minus 1 byte aws-chunked chunk;
multipart used 8 MiB chunks. This passed on the same MinIO release and TLS bridge
as the initial comparison. Cleanup verified zero objects and zero unfinished
uploads, then removed the temporary bucket and container. Independent readback
SHA-256 passed; the SDK still reported skipping multipart response-checksum
validation. TOS remains untested.

Raw evidence: `/tmp/hostel-s3-sdk-defaults-validation.log` and
`/tmp/hostel-s3-minio-sdk-defaults.log`.
