# Go cache ownership — #520

## Cause and evidence

The [preparation audit](preparation-profile.md) traced an immutable-cache race.
Native Linux packaging job
[102026005333](https://github.com/flidai/leapview/actions/runs/34215448215/job/102026005333)
saved the same setup-go key used by nine broad merge-validation jobs. Its Go
module-cache directory did not exist; the resulting archive was about 25 MB.
All nine validation jobs subsequently failed to reserve that key for their richer
contents. Other candidates restored the small archive and still downloaded tools
and application dependencies.

Before:

```text
native packaging ──> setup-go key X (small archive wins)
validation lanes ──> setup-go key X (cannot replace immutable contents)
```

Changing only the native writer would leave the old archive eligible for restore.
Giving all setup-ci callers one replacement key would still let narrower APIGen
or security work become the first writer. The fix scopes ownership by workload.

## Implemented ownership

`setup-ci` disables setup-go's automatic cache and uses the already-pinned
`actions/cache` action on the default branch and `actions/cache/restore` on
candidate refs for Go's module and build-cache paths. It resolves those
paths after Go installation, rather than caching a whole GOPATH or workspace.

The key combines:

- The semantic namespace `go-validation-v1`.
- Runner OS, architecture and hosted image identity.
- The actual installed Go version.
- The stable workflow job ID (`github.job`).
- Root and nested `go.mod`/`go.sum` files, plus `Taskfile.yml` and the setup action.
  These last two inputs include the pinned tool versions and SQLC's explicit
  Go 1.26.7 policy, alongside the main Go 1.26.8 toolchain.

After:

```text
native packaging ──> existing setup-go namespace
application lane ──> go-validation-v1 / go-application-validation
package lane ─────> go-validation-v1 / go-packages-validation
full extras ──────> go-validation-v1 / full-validation
APIGen lane ──────> go-validation-v1 / apigen-validation
frontend shards ──> go-validation-v1 / frontend-validation
```

These are compatible workload scopes, not run IDs or random cache busters. PR,
merge and nightly workflows already use matching job IDs for matching lanes
(full validation exists only in merge/nightly), so no caller or dependency
changes are necessary. The frontend matrix deliberately
shares a scope: each shard prepares the same generated inputs before its tests.
Other setup-ci callers own their own job scopes. Native packaging retains its
existing action and cache behavior.

The restore prefix retains the namespace, workload, OS, architecture, image and
installed Go version. It permits reuse across manifest and Taskfile changes:
Go's content-addressed build cache checks source and build inputs, module
versions have separate paths, and commands still request their pinned tool
versions. The exact save key continues to include all manifest/tool inputs.
No prefix bridges workloads or imports native setup-go archives. A miss runs
the same downloads, generation, tests and checks.

Only successful default-branch jobs publish at job completion. PR and merge-group
runs restore without saving large archives that other candidates cannot access.
This reduces eviction pressure on reusable default-branch archives. Equivalent
nightly writers may still race; the losing save is optional cache work, not a test
result. No cache output skips a validation command.

## Correctness and rollout

No caller workflow files, validation targets, required names, planner outputs, gate
conditions, permissions, runner configuration or merge dependencies change.
There is no producer barrier or shared workspace. Cache outputs are not used to
skip validation. Fresh-container flags, generated checks and native proofs stay
in place.

GitHub's existing ref visibility and save rules continue to apply. A queue or PR
cache is not a substitute for a trusted default-branch producer. Existing nightly
jobs can seed the same scopes on main, after which eligible candidates can restore
them. The new namespace deliberately starts cold; older entries need not be
removed to establish ownership.

Existing candidate archives can still occupy storage until GitHub evicts them. Cache
absence and save failures must remain performance-only outcomes. Source changes
with unchanged dependency identities may require fresh compilation even on an
exact hit; the immutable archive is not guaranteed to stay fully warm forever.

## Validation and expected impact

Validation completed locally:

- Focused cache contracts and the full architecture and CI planner/gate packages
  passed. Tests check cache ownership, manifest/tool identity, step ordering,
  stable workload IDs, native-cache separation and absence of cache-hit skips.
  An isolated fixture rejects the previous action configuration and passes with
  the new configuration.
- The cache-path script passed Bash syntax and execution checks with both
  `ImageOS` and the `/etc/os-release` fallback.
- Desktop tests passed: 141 Bun tests and 47 Node tests; the existing macOS-only
  optional-dependency test was skipped on Linux. Native installers were not
  rebuilt, and the native workflow/action configuration has no diff.
- Actionlint passed for all seven setup-ci consumers with the existing unsupported
  `stacked` event excluded. Documentation generation/checks and whitespace checks
  passed.
- `task ci` passed generation and SQL verification/audit, then stopped at required
  PostgreSQL conformance because this workspace cannot access Docker. No test
  requirement was disabled; the complete local CI contract has not passed.

Hosted cache restore/save behavior still needs confirmation on an eligible run.

The change removes the demonstrated reservation collision. It does **not** yet
establish a hosted elapsed-time reduction. The audit's 1–3 minute warm-path saving
is an experiment hypothesis; initial cold runs and larger restore/save costs can
reduce or erase it. The prior 21–23 minute merge observations remain the baseline
until an eligible hosted candidate tests this change.

For rollout, record each scope's producer/ref, archive size, restore/save outcome,
setup/preparation and test-command timing. Measure both cold and warm merge
candidates with unchanged required gates and exact-SHA native proof. Do not infer
a new p95 from one run or attribute the separate Buf-removal change twice.

## Cache publication follow-up — #667

See [ci-health-667.md](ci-health-667.md) for the September 22 inventory and
critical-path evidence behind default-branch-only publication and bounded
fallbacks. The earlier validation and rollout record above describes the initial
ownership change; it is not evidence that the follow-up meets the latency SLO.
