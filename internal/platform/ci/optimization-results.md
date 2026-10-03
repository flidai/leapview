# CI/CD optimization decisions

Progress and acceptance criteria are tracked in the
[Linear project](https://linear.app/flid/project/leapview-cicd-quality-and-speed-8a7e28e7113f/overview).
This is a decision record, not a declaration that integrated qualification is complete.

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
| Orchestration Nix archive | Benefit unverified; manual experiment only | [#814](https://github.com/flidai/leapview/pull/814) adds a guarded trusted producer and read-only consumers with archive/namespace budgets. Normal CI setup is unchanged. Hosted producer cost, warm performance, and later confirmations require the workflow to merge onto main. No broader validation cache is adopted. |

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

Current-main [nightly 37098873287](https://github.com/flidai/leapview/actions/runs/37098873287)
passed every validation lane on `c8f728eed2df1399d596d9184626c704c6aa5b22` except
dependency security. Its live Bun scans found high advisory
[GHSA-vfj7-8cjw-p6xm](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm) in root and
desktop `braces` graphs. The gate correctly failed; remediation and fresh
qualification are tracked in FAI-1059. No security exception is added.

Protected qualification of pending adoption commits and hosted warm Nix evidence
remain outstanding. GitHub requires a write-access approving review before queue
entry. Insufficient samples remain unverified. No experiment has twenty comparable
observations per mode, so this rollout makes no p95 claim. No release or live
infrastructure was published or modified solely to exercise orchestration.
