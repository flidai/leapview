# Performance regression governance

Performance qualification has two enforcement levels. The pull-request frontend
lane checks production JavaScript byte budgets after the existing build, and the
Go tests exercise missing evidence, incompatible baselines and deliberate latency
and memory regressions. The expensive immutable-image qualification measures
query, refresh, delivery, visualization and process resources. Passing the first
level does not substitute for the second.

Resource evidence retains the raw cold and warm checkpoint snapshots. A changed
process identity, falling CPU counter, missing measurement or summary that does
not match those snapshots makes qualification fail; it is not zero consumption.
Observed RSS and connection maxima cover checkpoints, not a continuous trace.

## Reference and candidate measurements

`.quality/performance-reference.json` pins a main-branch image by platform digest,
source commit and successful qualification run. `task image:qualify:performance
IMAGE=ghcr.io/flidai/leapview@sha256:...` measures that fixed reference first and
the candidate second on the same Linux amd64 runner. Both receive the current
benchmark harness and separate disposable application services. The reference
must pass absolute budgets and match its reviewed runtime identity before it can
be used for comparison. Failure to obtain a valid reference stops qualification.
`task image:qualify:production` delegates to this paired qualification as well.

The historical release/public installed-candidate journeys explicitly use
`bootstrap`: they retain their existing absolute checks on each architecture,
but do not claim relative performance qualification. Their success must not be
substituted for the separately recorded reference/candidate image pair. Extending
relative qualification to another architecture requires its own reviewed image
reference and calibration.

The `Main artifacts` workflow runs this pair and retains the identities and
reports for 90 days. It never promotes the candidate as the next reference.
Changing the reference requires a separate visible policy change with the old
and proposed images, commits, raw results, runner characteristics, variance
analysis and rationale. An architecture or toolchain change requires new
calibration; timings from incompatible environments are not normalized by CPU
count. The initial reference is Linux amd64 only. Other architectures require
their own reviewed reference and calibration before relative qualification can
be claimed.

The existing absolute ceilings and 1.25 latency ratio with a 50 ms meaningful
delta remain unchanged. They originated in the installed evaluator policy;
they are not newly measured universal limits. The new relative resource checks
use that ratio provisionally. Repeated normal-reference runs must establish
whether it distinguishes noise from regression before this project is complete.
A failing or noisy run is retained and investigated; it does not authorize
increasing the ratio, discarding samples or replacing the reference.

The [installed qualification runbook](https://github.com/flidai/leapview/blob/main/deploy/compose/QUALIFICATION.md)
specifies the sample counts, aggregation, cold/warm definitions and process
resource coverage. The supplementary MovieLens path provides higher-row-count
interaction evidence. Its local measurements are not production observations.

## Bundle budgets

`task build` produces `.tmp/frontend-bundle-evidence.json`. The logical entry
budgets include shared JavaScript dependencies; the aggregate counts each file
once, including the external Datastar runtime and MapLibre worker. Raw and gzip
bytes are measured independently. CSS, images and fonts are outside this
JavaScript policy.
The independent static-directory audit also accounts for the shipped theme and
login-loader scripts. Docker uses the same pinned Bun release as CI, checks the
budget in its web build stage, and retains the evidence in
`/usr/local/share/leapview/deployment/frontend-bundle-evidence.json`.

Platform and architecture are recorded but do not normalize byte counts: every
target must satisfy the same raw/gzip ceilings using the pinned Bun measurement
method. A passing byte check does not claim runtime equivalence or substitute
for that target's build and browser validation.

`task quality:frontend-bundle:check` validates the prepared evidence and enforces
both absolute ceilings and the relative ratchet in
`.quality/frontend-bundle-budget.json`. The calibration records repeated clean
builds and their source and toolchain identity. Identical build measurements
justify a zero-noise size ratchet; they do not justify a runtime timing claim.

`task quality:frontend-bundle:update` only tightens budgets. For an intentional
increase, produce a proposal with
`task quality:frontend-bundle:propose PROPOSAL=path.json REASON='explanation'`.
Retain before/after evidence, identify the added behavior and alternatives, and
obtain independent review before applying it. A proposal's requested limits must
match its measured candidate; it cannot introduce arbitrary extra headroom.

The policy embeds the active baseline's complete validated bundle evidence, a
canonical evidence SHA-256, and a decision audit. The initial record uses the
verified 57b0ae997 normal report and is explicitly pending GitHub review; its
local reviewer fields are audit context, not approval authority. Tightening or
applying a reviewed increase replaces the active evidence and budgets together,
while the historical calibration remains unchanged. Policy loading rejects a
missing, malformed, hash-mismatched, or byte-mismatched active baseline. The
current-head GitHub review guard remains the approval authority; this embedded
record does not by itself claim tamper-resistant governance.

## Review and delivery

The required `CI gate` includes performance baseline review. Changes to a
performance policy, reference or evidence checker require an approval from a
different human repository collaborator at the current PR head. A local review
field, a bot approval, a dismissed approval or an approval of an older commit
does not satisfy it. The review step runs inside the required CI gate, before
the planned-results check, including for deferred or selectively validated PRs.
After approval, rerun the failed CI gate job. Once the guard
is on main, CI executes the base branch's copy when evaluating candidate changes.
Initial installation of the guard itself requires review of that first PR;
the base branch has no trusted checker yet, so the first PR's check alone is
not evidence that the checker is protected against candidate modification.

This is repository CI enforcement, subject to the repository's normal workflow
and administrator trust boundary. It does not change GitHub repository settings.
The current repository rules allow zero approving reviews. Consequently a
candidate could modify the workflow that invokes this checker. Before claiming
tamper-resistant baseline governance or project completion, a repository
administrator must approve and activate an independent protection: require
current-head review of the governance paths and their workflow/Taskfile entry
points (including stale-review dismissal), or a required organization-controlled
workflow running a trusted checker. Verify that protection with an unapproved
gate-removal PR. This activation is an explicit external delivery dependency;
the PR-local guard does not replace it.

Before delivery, independently review the full diff, run `task ci` and
`task ci:full`, retain the normal and controlled-regression evidence, and verify
hosted checks on the final commit. A local implementation or a passing unit test
does not mark runtime qualification, approval or merge complete.
