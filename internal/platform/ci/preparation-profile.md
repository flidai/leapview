# CI preparation and cache audit — #520

Audit date: 2026-09-08. **Documentation only; no cache, workflow, task, runner or
validation changes are made by this audit.**

## Result and measurement scope

Preparation materially contributes to merge latency: application setup plus
preparation takes **5m11s–6m13s**, and full-validation runner setup plus preparation takes **6m02s–6m24s**. Each
candidate runs the same `ci:prepare` on eight isolated runners, spending
**31m39s–31m40s of aggregate runner time** on that task. Those costs overlap;
they are not 31 minutes on the merge critical path.

The strongest finding is a **cache ownership collision**, not a missing cache.
A native packaging job populates the same Go key as the broad validation lanes
with a small compiler cache and no module directory. The richer lanes cannot
replace it. Fixing ownership and warm-cache publication is the first experiment
to design. Centralizing preparation alone primarily saves runner time and adds a
new dependency barrier; it is not an established elapsed-time improvement.

Evidence comes from all **27 non-gate jobs** in these three completed, successful,
attempt-1 `merge_group` runs:

- [34212294501](https://github.com/flidai/leapview/actions/runs/34212294501), SHA `13e061045193ac0fc794ee4112e9ab0939f66fbb`.
- [34215268411](https://github.com/flidai/leapview/actions/runs/34215268411), SHA `0f50134925ac4721443751c6a313985528564416`.
- [34215448203](https://github.com/flidai/leapview/actions/runs/34215448203), SHA `a1c287f409620172938c03637daad646c6a29c64`.

These are the deployed Phase 2.3 cohort in
[hosted-remediation-measurement.md](hosted-remediation-measurement.md), observed at
**22m02s, 21m09s and 22m41s**, respectively. They predate the separate Buf-removal
follow-up in PR #540; its expected saving is not credited here. These samples do
not establish a new p95. The historical pre-#520 p95 is **44m40s**.

## Per-lane preparation cost

Each row contains three observations; values are min–max, not percentiles.
Setup is the composite toolchain step; preparation is the explicit generated/
embedded-assets step. Test execution means the validation-command step envelope,
including compilation and any nested setup; package validation also includes
Prometheus and generated-artifact checks. GitHub does not expose pure test CPU or
network-download time separately. APIGen has no standalone `ci:prepare` step;
its own module preparation occurs inside its validation command.

| Lane | Setup | Preparation | Test execution / validation envelope |
|---|---:|---:|---:|
| APIGen | 1m43s–2m00s | 0s | 2m29s–2m40s |
| Full merge validation | 2m02s–2m17s | 4m00s–4m07s | 14m46s–15m12s |
| Frontend / chat | 2m02s–2m25s | 4m00s–4m10s | 16s–17s |
| Frontend / core | 1m59s–2m04s | 3m16s–4m01s | 35s–38s |
| Go package | 1m55s–2m00s | 3m24s–4m02s | 9m48s–16m17s |
| Frontend / data | 2m03s–2m34s | 3m55s–4m19s | 34s–37s |
| Frontend / reports | 2m08s–2m19s | 4m00s–4m08s | 48s–51s |
| Go application | 1m45s–2m03s | 3m26s–4m10s | 14m26s–15m44s |
| Frontend / site | 2m00s–2m29s | 3m20s–4m10s | 40s–50s |

Checkout takes **5–18s** across the cohort, usually 5–7s. Long-lane runner queues
are **3–4s**. Full extras cleanup ranges **2–50s**; application **1–23s**, packages
**1–21s**, with costly cache-save attempts on the cold-key candidate. Neither
checkout nor queue scheduling explains the roughly 22-minute critical path.

## Where work repeats

`Taskfile.yml` defines `ci:prepare` as extension fixture provisioning and supply
checks, complete generation, browser build, then public-site build. Application,
packages, full extras and all five frontend shards run it separately. APIGen is
independent. Task's local checksum/up-to-date state does not cross runners.

1. **Go tools and dependencies:** setup compiles pinned Task (and Buf in this
   historical cohort). All 27 logs still contain **215–395 `go: downloading`
   records**, including after cache hits. The counts include tool dependencies
   and are not unique-module counts or network durations. Root-module and nested
   APIGen dependencies, SQLC's pinned tool module and the Go 1.26.7 SQLC toolchain
   are distinct from the Go 1.26.8 application compiler.
2. **Generated contracts and executable compilation:** every prepared runner
   generates SQLC, API/signals, schemas, documentation and browser contracts.
   Application job [102025424924](https://github.com/flidai/leapview/actions/runs/34215268411/job/102025424924)
   spends **76s** between SQL generation and the next command and **59s** between
   schema export and schema-doc generation. These are build/startup/generation
   envelopes, not 135s of proven avoidable work.
3. **Frontend assets:** every prepared runner installs the frozen Bun graph and
   builds both application and site assets, including backend lanes that embed
   generated assets. Five frontend jobs spend roughly four minutes preparing
   inputs for validation envelopes of **16–51s**. The existing
   `ci:prepare:frontend` target is a candidate starting point, not a proven drop-in
   replacement for every shard: all generated/site/docs consumers need mapping.
4. **Extension fixtures:** each fresh runner provisions its own private DuckDB
   extension directory and validates supply contracts. The tool verifies versions,
   loads artifacts and calculates digests. Local reuse exists; GitHub cache
   persistence for this directory does not. Private permissions and runtime/OS/
   architecture compatibility must survive any future transport.
5. **PostgreSQL and Docker:** database containers, migrations, authentication and
   readiness occur in validation commands, not in the explicit preparation column.
   Application PostgreSQL conformance alone takes **11m58s–12m58s**. The source-
   derived inventory runs with `-p 2`, integration tags, required containers and
   `-count=1`. Startup and actual assertions are interleaved, so this is not a
   measured 12-minute image-pull cost. Disposable databases and concurrent-test
   ownership are correctness boundaries, not redundant artifacts.

Command profiling in the nine application/package/full logs separates the main
preparation costs (Task marker intervals, including subprocess compilation):

| Preparation component | Observed interval |
|---|---:|
| Initial DuckDB provisioning plus supply check | 32.6–42.6s |
| Complete generation graph | 168.2–205.2s |
| SQL generation, within that graph | 71.3–93.3s |
| Schema generation, within that graph | 55.5–70.7s |
| Browser build | 1.0–1.8s |
| Site build | 0.84–1.22s |

Do not add nested rows to the generation total. A second extension invocation
inside preparation is already warm and costs **0.56–0.84s** for that command;
the nearby **5–7s** visual-doc generation is required work, not extension download
duplication. These results favor Go preparation over caching final browser assets.

The same-run runner-time arithmetic is:

| Run | Preparation copies | Sum of preparation | Slowest copy | Sum minus slowest copy |
|---|---:|---:|---:|---:|
| 34212294501 | 8 | 31m40s | 4m17s | 27m23s |
| 34215268411 | 8 | 31m40s | 4m08s | 27m32s |
| 34215448203 | 8 | 31m39s | 4m19s | 27m20s |

The last column is a hypothetical runner-time budget before artifact transfer and
consumer initialization. It is **not** expected merge-latency reduction.

## Existing caches and observed effectiveness

Counts below are exact-key restores among jobs where the cache was enabled,
not repository-wide hit rates. Current source is the shared setup action plus
the other workflow/action cache consumers; sampled execution uses the exact SHAs
above. Pinned setup-go v6 caches `GOMODCACHE` and `GOCACHE`, with OS/architecture,
runner distribution, Go version and root `go.sum` identity. Its
[restore implementation](https://github.com/actions/setup-go/blob/924ae3a1cded613372ab5595356fb5720e22ba16/src/cache-restore.ts)
is the reference, rather than a newer action's defaults.

| Cache | Sample exact hits | Identity / scope | Interpretation |
|---|---:|---|---|
| Go modules + compiler outputs | **18/27 (66.7%)** | Shared setup-go key; root dependency checksum | All nine jobs hit in each of the first two runs; all nine miss on changed checksum in the third |
| Bun package downloads | **27/27 (100%)** | OS/arch, Bun 1.3.14, root + desktop lock hashes; version-scoped fallback prefixes | Frozen installation still runs; a hit is not permission to trust an existing `node_modules` tree |
| Playwright Chromium | **18/18 (100%)** | OS/arch + Playwright 1.61.1 | Five frontend shards and full extras use it; system dependency installation still runs |
| Terraform providers | **3/3 (100%)** | OS/arch, Terraform 1.13.5 and deployment lock hashes; version-scoped fallback | Full extras only; init/validation still execute |
| Bun executable / hosted Node tool cache | Present in setup logs | Tool-version/platform identities owned by setup actions | Different from Bun package-download cache; not counted as package hits |
| Docker BuildKit layers | Not exercised by this merge cohort | Production, site and release workflows use `gha` scopes by image/architecture | These do not populate Testcontainers' daemon image store on merge runners |
| Python/pip | Not measured here | Reference/selected physical-contract workflows use setup-python caching | No hit-rate or merge-latency claim from this cohort |

Composite substep log intervals across the 27 jobs: Go setup **7.9–13.3s**, Bun
package-cache restore **3.1–6.2s**, historical Task+Buf install **83.6–114.0s**.
The 18 browser-enabled jobs spend **12.3–20.4s** on cached Chromium/system
installation. Rebuilding package trees or downloading images is not automatically
expensive enough to justify a new archive cache.

### Verified Go cache writer collision

The first two candidates restore root checksum `7544f7e…`; the main cache
**7364966855** is **24,846,316 bytes**, created 2026-09-05T16:44:48Z. The full key
and one restore/save trace appear in the previous profiling report.

For candidate `a1c287f…`, the root checksum changes to `64273d8f…`. All nine merge
jobs log a miss. The exact-SHA native proof's
[Hardened package (Linux x64) job 102026005333](https://github.com/flidai/leapview/actions/runs/34215448215/job/102026005333)
then records:

- 10:28:19.506 UTC: its module cache directory does not exist.
- 10:28:21.257: it saves
  `setup-go-Linux-x64-ubuntu24-go-1.26.8-64273d8f9f03fde2dbeeb996a9be4e1eb853a5064b32734355e4c71c0e887c9b`.
- GitHub cache **7450424932**, on the PR #534 queue ref, has the same creation
  timestamp and size **24,844,097 bytes**.
- APIGen at 10:31:12 and application at 10:48:55 fail to reserve that same key
  for saving; the larger application cache cannot replace the earlier entry.

`.github/actions/desktop-preview-candidate/action.yml` and `setup-ci/action.yml`
use the same cache-enabled setup-go configuration despite very different Go
workloads. This establishes the writer for this queue cache, not the original
writer of historical main cache 7364966855. A later main entry for the new checksum
also measures about 25 MB; its writer was not traced here.

GitHub caches are immutable per key and restore within permitted ref scopes;
PR merge-ref entries are not a way to publish a reusable main cache. Matching
keys do not transfer a just-produced workspace directly between concurrent jobs.
A default-branch producer and a deliberate new cache generation may be needed to
make an ownership fix useful across future merge candidates.
[GitHub cache reference](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching)

## Sharing options and correctness

| Work | Shareable form | What must remain local / mandatory |
|---|---|---|
| Downloaded Go modules | Same compatible dependency identities, including nested modules and pinned tools | Dependency integrity checks; missing entries fetched normally |
| Go build cache | Trusted compatible compiler/platform/CGO context; separate broad-validation producer from narrow native probes | Do not use a hit to skip gates; retain forced fresh external-service tests |
| Generated sources and browser/site assets | Allowlisted immutable artifact from the same tested SHA, run and attempt; producer validates outputs | Exact provenance, completeness, generated-diff checks, required consumers and failure propagation |
| Bun downloads | Existing shared version/platform cache | `bun install --frozen-lockfile` and a single writer per checkout remain |
| Browser and provider downloads | Existing caches, retaining pinned identities/lock validation | Host libraries, Terraform init and validation |
| Container image bytes | Potential digest-pinned image pull reuse after separately measuring pull cost | Fresh containers, ports, networks, volumes, migrations and readiness |
| PostgreSQL data directories, dev servers, `.tmp`, test evidence | **Do not share** as cached preparation | Fresh state, isolation, cleanup, runtime credentials and per-candidate proofs |
| Task checksum markers / cached test results | Do not transplant as proof of validation | Verify generated outputs and preserve the intended fresh-test flags |

Sharing a common module key is possible, but only if its writer actually prepares
that dependency set. Broad validation and narrow native packaging should not race
to freeze the same immutable archive. Separate namespaces or producer ownership
must still respect branch trust and default-branch publication. A key change alone
can repeat the same race, and disabling a writer alone leaves existing immutable
entries in place. A future key proposal should cover nested module checksums,
pinned tool versions and both compiler versions, rather than relying only on
root `go.sum`. Keep download identity distinct from compiler/platform identity
where that reduces churn; choose additional lane scopes only from measured
cache contents, not one archive per job by default.

Compiler caches can also contain Go test results. Go determines reuse from inputs
and flags, while external state and C libraries have caveats; this is not a reason
to replace required container runs with cache hits. Preserve the inventory's
`-count=1`, fixture verification and fresh proof commands.
[Go build and test caching](https://pkg.go.dev/cmd/go#hdr-Build_and_test_caching)

Dependency caches and executable artifacts are a trust boundary. Never include
credentials, database state or whole workspaces. Limit publication to trusted
contexts, retain package integrity checks, and do not allow a low-trust PR to
supply executable cache contents to a privileged release path. The current
configuration does not establish a cache vulnerability, and this audit does not
claim one. Cache absence, eviction or corruption must produce normal validation
or a visible failure, never a successful skipped check.
[GitHub cache security guidance](https://docs.github.com/en/actions/concepts/workflows-and-actions/dependency-caching)

## Candidates, expected reduction and risk

No cold-versus-warm controlled run has been performed. Numeric estimates below
are experiment hypotheses or budgets, not measured improvements or new p95s.

| Priority | Candidate | Expected effect / measurement budget | Risk |
|---:|---|---|---|
| 1 | Separate native-probe cache ownership; publish a populated broad-validation Go cache from a trusted producer | Test a **1–3 minute** warm-path reduction across the long lanes, based on repeated tool/SQLC/schema compile envelopes; restore/save overhead and partial warming may erase some or all of it | Medium: ref publication, toolchain identity, cache size and test-result policy |
| 2 | Same-run artifact for complete generated/embedded inputs | About **27 minutes of runner work** is a theoretical reuse budget per candidate; retained local regeneration reduces that saving, and elapsed saving is unproven and could be negative after producer queue/setup and transfer | Medium/high: new barrier, missing outputs, provenance, failure fan-out |
| 3 | Verify a smaller frontend preparation contract using the existing target | Up to roughly four minutes per frontend runner is the total prep budget, not all removable; little merge benefit while app/full remain blockers | Medium: hidden generated consumers and docs/site contracts |
| 4 | Avoid proven repeated extension preparation within one task invocation | Less than one second for the measured warm extension command; not a worthwhile target alone | Low/medium: mutable fixture inputs and task ordering |
| 5 | Add image or installed-dependency archives | No defensible savings estimate yet; current Bun/browser restores are already effective at finding entries | Medium: transfer cost, platform mismatch and stale state |

An artifact proposal must also account for existing dependency execution:
`generated:check` depends on generation, and `db:check` intentionally regenerates
SQL twice before verification and live SQL preparation. Downloading files does
not by itself prevent these tasks from rerunning. Never transplant Task's skip
markers or omit those checks to manufacture the theoretical runner-time saving.

The first experiment should preserve every validation command and required name,
record cache archive size/producer/ref, restore time, tool install, SQLC/schema
commands, test envelopes and cleanup, and compare cold and warm runs at matching
source/toolchain inputs. Do not count PR #540's independent Buf removal twice.
A warm candidate must still run all mandatory validation. Only after collecting
such evidence should the production cache design be selected.

Even eliminating explicit setup/preparation leaves long validation envelopes of
roughly 14–16 minutes in these samples. Preparation work is material, but this
audit cannot promise the 12-minute target: build warming inside tests may help,
and PostgreSQL/full-extras execution may still require separate engineering.

## Reproduction and audit validation

Collected read-only GitHub REST job details and logs for each run above:

```text
GET /repos/flidai/leapview/actions/runs/{run}/attempts/1/jobs?per_page=100
GET /repos/flidai/leapview/actions/jobs/{job}/logs
GET /repos/flidai/leapview/actions/caches?key={exact_key}&per_page=100
```

Job and step timestamps, cache markers, Task command boundaries and the native
writer log were checked independently against workflow/Taskfile source. Raw logs
stay outside the repository. Existing reports retain candidate eligibility and
full job timestamp evidence. Validation: `bun run docs:validate-diagrams` passed (7 existing diagrams),
`docsitegen --check` passed with a task-local temporary directory, and
Markdown table-column and whitespace checks passed. No full CI was run for this audit-only file.
