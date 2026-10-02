# CI optimization rollout

Baseline captured on 2026-10-02 before execution changes, from the GitHub Actions
run-attempt jobs API, the PR planner log, and the cache listing API. The full
job/step timestamps, runner labels, workflow blobs, tested revisions, event,
attempt, and cache snapshot are in `measurements/optimization-baseline.json`.
Workflow revisions denote the event checkout revision; workflow file content
identity is independently recorded as a Git blob. Runner labels record platforms;
GitHub's API does not expose the complete hosted image version.

| Population | Run | Result | Completion seconds | Initial queue seconds | Runner minutes |
| --- | --- | --- | ---: | ---: | ---: |
| PR branch graph | 36997034068 | success | 1840 | 0 | 94.58 |
| Merge candidate graph | 36994619763 | success | 1044 | 0 | 110.07 |
| Earlier main nightly graph | 36984943767 | failure | 1329 | 0 | 141.13 |
| Current main image graph | 36996263673 | success | 1705 | 0 | 38.13 |

Completion is `updated_at - created_at`; runner minutes sum job execution
intervals, including setup and teardown. Initial queue is
`run_started_at - created_at`. Job start offsets also contain dependency waits
and cannot establish per-job queue delays. Cache sizes are a point-in-time
snapshot, not attribution of bytes restored by these earlier runs. This is a
baseline inventory, not a paired performance comparison or a p95 estimate.
PR head revision differs from its tested merge revision; the latter is verified
from the planner log. Branch graphs must be compared separately from main.

The baseline nightly failure is in `Nightly dependency security` / `Run dependency
security scans`, with the strict gate also failing. Its failure log was unavailable
through `gh run view --log-failed`; no vulnerability identifier or remediation is
inferred. Dependency upgrades remain outside this project. Open Nix candidate work
(PR #804) and site observation work (#803) were checked for overlap; their
application, signed-evidence, and live-observation changes are outside this stack.

Retained coverage inventory:

| Contract | Owners and invariant |
| --- | --- |
| PR selection / gate | `ci.yml`, typed planner/gate tests: drafts, cumulative/deferred stacks, selective/full/unknown paths, exact plan head/run/attempt/artifact ID, successful-plan partial reruns |
| Exhaustive merge / nightly | `merge-validation.yml`, `nightly.yml`: APIGen, packages, application, all five frontend shards, full extras; recovery/transition required in both after this correction |
| Native desktop | `electron-security-proof.yml`, preview candidate action: Linux/macOS Intel/macOS ARM/Windows package proofs; exact merge-SHA desktop proof joins merge gate |
| Security | `security.yml` keeps Security gate; nightly dependency scans/evidence refresh and OCI freshness/admission checks retain separate purposes and failure signals |
| Direct Nix | `nix-development.yml`: development suite, image/registry checks, runtime SBOM/vulnerability checks and signed evidence retained |
| Generation / services | Taskfile forced regeneration/snapshot checks, generated:check, native PostgreSQL, MinIO, warehouse/spatial and recovery qualification retained |
| Publication / deployment | release, artifacts, site-image/site-deploy, demo/Hetzner and infrastructure workflows retain exact revision stamping, image admission, attestations, prepromotion qualification and operator protections |
| Independent boundaries | managed scaffold, public-site smoke, recovery evidence, dbt reference/Azure and local Docker macOS retain entrypoints and validation responsibilities |

The rollout has three focused, stacked PRs: qualification correctness;
orchestration and task ownership; image-layer reuse. Existing Go cache ownership,
release/deployment policy, the full Nix development suite, and independent
security checks remain in place.

Higher-risk experiments remain measurement-gated:

- Nix caching: first measure the orchestration closure and realization cost.
  Before adding a pinned GitHub cache action, specify a default-branch producer
  that verifies the checked-out event SHA, restore-only candidate/alternate-source
  consumers, platform/profile/toolchain keys, byte budget, and retention. Publish
  only toolchain closures before application execution. Compare cold/warm,
  changed inputs, and unavailable-service runs including transfer/producer costs.
  Measure validation closures separately before considering expansion.
- Go caching: keep workload keys and joint module/build archives. Measure archive
  composition, transfer time, and useful reuse before designing a replacement.
  Do not delete namespaces until replacement consumers succeed on PR and merge.
- BuildKit mounts and release-main reuse: measure each independently, retaining
  architecture/compiler/application context and same-tag caching. Do not add ARM
  warming workloads without recovered cost. Preserve admission and attestations.
- Recovery concurrency: measure successful latency and failed-run extra work
  independently. Both provider and full jobs need explicit draft/manual guards.
- Nix suite consolidation: requires executable equivalence evidence for shell
  export, generation, CGO, browsers, services, and deferred stacks.

Each adoption requires at least three paired runs holding source, toolchain,
platform, and selected work constant: at least 10% median improvement in execution
time or runner usage, with at most 5% regression in the other. Include producer and
transfer costs and separate queues. Confirm over ten comparable subsequent runs;
do not report p95 from fewer than twenty observations. No hosted performance
experiment or release publication is implied by local contract tests.

Release qualification jobs are downstream of publication. Use GitHub's failed-job
or individual-job rerun controls to retry qualification while retaining successful
publication. Rerunning the entire release workflow intentionally continues to
refuse replacement of an existing release.

References: [reusable workflow permissions](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows),
[job reruns](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs),
[release listing](https://docs.github.com/en/rest/releases/releases#list-releases).
