# GitHub-hosted CI

LeapView runs continuous integration on standard, ephemeral GitHub-hosted runners. GitHub
owns runner provisioning, isolation, queueing, logs, permissions, cancellation, and cleanup.
The repository owns the pinned toolchain setup, bounded remote caches, Taskfile commands, and
validation tiers that define a valid change.

Every job starts from a clean Ubuntu image and loses its local filesystem when it completes.
Correctness therefore cannot depend on an old checkout, Docker daemon, image, volume, or
tool cache. This prevents accumulated runner state from making a later run fail or pass.

## Trust boundary

Internal, external, and Dependabot pull requests use the same GitHub-hosted execution path.
The pull-request workflow grants only `contents: read`; it does not expose deployment or
registry credentials. Fork code never executes on a Flid-managed host.

GitHub Actions caches contain only reproducible dependency downloads, compiler outputs,
browser binaries, Terraform providers, and BuildKit layers. They never contain credentials,
runtime databases, Docker state, or qualification evidence. Cache restoration is a
performance optimization: deleting every cache must still produce the same result.

## Execution contract

Four Taskfile targets define the validation tiers:

```console
task ci
task ci:pr
task ci:full
task ci:nightly
```

`task ci` is the local alias for the fast pull-request contract. `task ci:pr` prepares shared
inputs, runs the bounded Go and frontend lanes, and checks generated artifacts. `task ci:full`
adds desktop tests, static and selected race analysis, route QA, and deployment validation.
`task ci:nightly` adds dependency and vulnerability scans. `task ci:local` remains a
compatibility alias for the full current-machine contract.

GitHub Actions distributes those same Taskfile units across clean runners. Pull requests run
APIGen, the non-application Go packages, sharded application tests, and frontend validation
concurrently. The three repository lanes execute `task ci:prepare`; the independent APIGen module
does not need generated or embedded application assets and skips that preparation. The merge queue
adds `task ci:full:extras`, and the daily schedule also runs `task ci:nightly:extras`. Local
composition remains available through the tier targets; the workflow does not duplicate individual
test commands or introduce a runner-specific container wrapper.

## Toolchain and caches

`.github/actions/setup-ci` installs pinned Go, Node.js, Bun, Task, and Buf versions. Jobs opt
into the pinned Terraform and Playwright installations only when their validation requires
them. The action is shared by pull-request, merge, nightly, and production qualification jobs
so a toolchain change has one reviewable source.

The setup action uses separate GitHub Actions cache entries for:

- Go modules and compiler outputs, keyed by the Go dependency graph;
- Bun downloads, keyed by the root and desktop lockfiles;
- the pinned Playwright Chromium build;
- Terraform providers, keyed by the deployment lockfiles.

Production and public-site image builds export BuildKit layers to independently scoped
GitHub Actions caches. LeapView does not cache `node_modules`, `/var/lib/docker`, mutable
worktrees, or application data. Exact keys are immutable, restore prefixes may seed a new
dependency set, and the package manager or build tool always validates restored content. The
main artifact workflow populates the default-branch Bun download cache so new pull requests do
not inherit an empty cache entry from image qualification.

The repository currently works within GitHub's default 10 GB cache allowance. The intended
operating limit is 50 GB or more so the independent Go, Bun, browser, Terraform, and BuildKit
caches do not evict one another. Hitting the lower limit may reduce cache hits but cannot
change validation behavior.

## Workflow tiers

The pull-request workflow runs APIGen, Go package, Go application, and frontend validation on
independent four-vCPU runners and reports the stable required `CI gate` check. This prevents browser
and Go test contention and shortens wall-clock feedback without increasing per-job machine size.

For a native GitHub pull-request stack, only the top pull request runs those validation lanes.
Lower layers report a successful `CI gate` with a summary that validation is deferred to the
stack tip. The workflow listens for the `stacked` action, and its concurrency key uses the native
stack ID, so rebasing a stack cancels obsolete feedback for the whole stack instead of filling the
runner queue. Standalone pull requests and manual dispatches continue to run both lanes.

The main-branch ruleset must require GitHub's merge queue. A deferred lower-layer gate is feedback,
not authorization to merge directly. The queue validates the exact candidate selected for main,
whether that candidate is the complete stack or a contiguous prefix, by running the Go and frontend
lanes plus the full extras. Merge-validation concurrency is scoped to the candidate ref: a rebuilt
candidate cancels its obsolete run without making distinct queue candidates cancel one another.

Nightly CI also runs security scans in parallel. Post-merge artifact CI builds and pushes the
production image using a BuildKit cache, then qualifies its immutable digest on a second clean runner.

Splitting production build and qualification prevents build layers from consuming the local
disk needed by the qualification journey. The digest passed between jobs is the only product
identity boundary; qualification never falls back to a mutable tag or source build.

## Operations

There is no persistent CI VM to patch, prune, or recover. Monitor queue time, cache hit rate,
cache eviction, job duration, and GitHub service health. A cache outage or eviction should
make a job slower, not fail it. If a clean GitHub runner lacks enough per-job CPU, memory, or
disk, split independent work into jobs before introducing persistent infrastructure.
