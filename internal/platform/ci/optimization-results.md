# CI/CD optimization decisions

Progress and acceptance criteria are tracked in the
[Linear project](https://linear.app/flid/project/leapview-cicd-quality-and-speed-8a7e28e7113f/overview).
This records measured decisions and exact protected qualification. Production
orchestration adoption completed in #821 with protected qualification and a warm rollout.
See the [repository-wide reassessment](reassessment-2026-10-03.md) for subsequent findings.

The initial rollout merged as [#805](https://github.com/flidai/leapview/pull/805),
[#807](https://github.com/flidai/leapview/pull/807), and
[#808](https://github.com/flidai/leapview/pull/808). Required CI, security, native,
recovery, and transition checks passed on their exact merge candidates. The
complete Nix development suite remains required. Distinct security rescans,
forced-fresh service tests, revision stamping, image admission, attestations, and
qualification before promotion remain independent of cache success.

| Experiment | Decision | Evidence and limits |
| --- | --- | --- |
| Ordinary release amd64 layers | Retain production-main reads after the existing release scope | Three screening pairs and 12 later comparable consumers per mode passed. Later median build execution improved 51.83%; full runner cost remained within 5% under the explicit entire-producer / three-consumer allocation. See [release-cache-reuse.md](release-cache-reuse.md). ARM warming and cache writers are unchanged. |
| Persistent BuildKit compiler mount | Reject; keep current mounts | [37098049134](https://github.com/flidai/leapview/actions/runs/37098049134): restore/inject/build median 254 → 263 seconds; consumer runner 284 → 314; full 742-second seed allocated over three consumers gives 561.33 seconds (+97.65%). The 949,937,930-byte archive held 4,017,129,093 logical bytes. Faster build-only timings do not recover extraction, transfer, injection and producer cost. |
| Go archive restructuring | Retain existing joint workload archives and keys | Three successful warm assessment jobs in [37100477694](https://github.com/flidai/leapview/actions/runs/37100477694) restored current archives published by successful main nightly jobs. Package/application/full archive restores took 25.347 / 27.040 / 25.799 seconds. Controlled compiler-only probes used 0 / 6 / 6 compiler invocations versus 319 for empty build caches. This establishes useful reuse, not whole-workflow savings. |
| Recovery concurrency | Reject; retain provider → full-validation dependency | Three success pairs had 33.61% lower median execution, but the required three-success/one-injected-failure mix consumed 12,847 → 15,557 runner seconds (+21.09%). Median runner usage regressed 6.44%, exceeding the 5% limit. This test mix is not an estimate of production failure frequency. |
| Orchestration Nix archive | Retain for planner/gate setup only | Three screening pairs and 12 later comparable pairs passed. Later affected setup improved 62.92%; whole-operation producer / three-consumer allocation improved runner cost 24.08%. See [orchestration-cache.md](orchestration-cache.md). Consumers restore only; the default-branch producer has bounded retention. Full validation uses the complete shell. |

Go assessment inputs and compiler/CGO identities, composition, compressed sizes,
restore costs, and actual compiler invocation counts are retained in
`measurements/go-cache-utility.json`. Its independent assessment jobs succeeded;
the unrelated provider/full wrapper was intentionally cancelled after collecting
them. It is not a whole-workflow qualification pass. Cold assessment misses were
correct conservative toolchain invalidation; keys were not relaxed to restore an
older namespace. Exactly 11 obsolete v1 main archives totaling 20,572,733,591 bytes
were deleted after verifying replacement caches had served PR and merge runs.
Current v2 archives were retained; no backend reclamation or billing claim is made.

`measurements/buildkit-mount-screening.json` records the corrected compiler-only
mount probe. The earlier joint module/compiler seed failed completeness and is
excluded from performance measurements. Every successful consumer retained exact
source, compiler/platform, archive integrity and local-image identity checks.

`measurements/recovery-concurrency-screening.json` records each run/attempt and
active job cost. Provider/full jobs used the same Go 1.26.8 / Linux amd64 / CGO
context and identical cold workload-cache key. Serial failure skipped full work;
parallel failure remained visible despite an independently successful full job.
[37099434676](https://github.com/flidai/leapview/actions/runs/37099434676) records
the cancellation case and is excluded from performance statistics. Initial queue
and active job span are separate from job execution costs.

The security remediation [#815](https://github.com/flidai/leapview/pull/815),
release cache adoption [#816](https://github.com/flidai/leapview/pull/816) and
fresh-store producer repair [#819](https://github.com/flidai/leapview/pull/819)
merged through the protected native stack queue on October 3, 2026 at 13:44 IST.
Every exact candidate passed full CI, dependency security and all four native
platform proofs, including recovery and schema transition qualification.
`measurements/optimization-qualification.json` records their nine immutable
run/attempt/source receipts. No checks failed or were rerun for this stack.
The earlier #814 native qualification reran only failed jobs on the same candidate
after an artifact-upload DNS failure; the build and qualification had succeeded.

The pre-remediation nightly scans correctly rejected high advisory
[GHSA-vfj7-8cjw-p6xm](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm) in root and
desktop `braces` graphs. #815 removes that transitive chain through the current
native watcher and adds a real include/ignore filesystem integration check.
Fresh source nightly [37102498752](https://github.com/flidai/leapview/actions/runs/37102498752)
and the complete five-job Nix suite [37103507483](https://github.com/flidai/leapview/actions/runs/37103507483)
passed. No advisory exception or freshness relaxation was added. Main
[c5aea6cc](https://github.com/flidai/leapview/commit/c5aea6cc2768cffa504fc3000a3d235a098da1af)
then passed image and schema predecessor qualification in
[37109042166](https://github.com/flidai/leapview/actions/runs/37109042166).
A fresh merged-main nightly [37110933016](https://github.com/flidai/leapview/actions/runs/37110933016)
passed every required lane, including live dependency scans, full validation,
recovery and schema transition. The earlier failed nightly used pre-remediation
main and is not a failure of the fixed source.

Production orchestration adoption merged in #821 and passed trusted main
publication and warm rollout verification; the final integrated receipts are linked
from the [reassessment](reassessment-2026-10-03.md). GitHub requires an independent
write-access approving review before queue entry. No experiment has twenty comparable observations per mode, so this
rollout makes no p95 claim. No release or live infrastructure was published or
modified solely to exercise orchestration.
