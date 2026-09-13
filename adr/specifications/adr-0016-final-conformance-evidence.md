# ADR-0016 final conformance evidence

Status: qualification complete; active implementation remains partial

Last updated: 2026-09-13

Governing decision: [ADR-0016](../0016-adopt-standards-aligned-data-contracts-and-interchange.md)

Tracked by: [FAI-632](https://linear.app/flid/issue/FAI-632)

## Scope

This ledger qualifies the standards-aligned contract and interchange work
already merged for FAI-616, FAI-619, FAI-620, FAI-622/662, FAI-623, FAI-629,
and the FAI-649 activation dependency. It adds no adapter, runtime transport,
policy evaluator, identity authority, or migration.

ADR-0018 Project namespace, ResourceUID/tombstone lifecycle, recovery, and dbt
evidence are explicitly outside this qualification and are not used to claim
ADR-0016 completion.

## Acceptance ledger

| Requirement | Implementation owner | Evidence | Status | Remaining boundary |
|---|---|---|---|---|
| Authored analytics/control-plane boundary | FAI-616, PR [#572](https://github.com/flidai/leapview/pull/572) | Removed-kind discovery/schema tests; dashboard-publication deployment guard; architecture ownership guard | Implemented | Project namespace and ResourceUID lifecycle remain ADR-0018-owned. |
| Generated structural authority and governance metadata | FAI-619/632, PRs [#472](https://github.com/flidai/leapview/pull/472) and [#516](https://github.com/flidai/leapview/pull/516) | TypeSpec-generated Go/JSON Schema; metadata, stable-check-ID, structural-authority, `TestSourceDeprecationContext`, `TestModelDeprecationUsesContextualValidation`, and publication replay tests | Implemented | Source and Model projections reject missing/self replacements, replacement cycles, and `deprecation.since` later than the containing contract version. |
| Canonical Source/Model/SemanticModel projections | FAI-620/662, PR [#534](https://github.com/flidai/leapview/pull/534) | Reconciled CAN/SRC/MOD/SEM/SER rows in the versioning ledger; sealed projection, exclusion manifest, analyzed SQL, typed normalization, RFC 8785 and independent fixture tests | Implemented | Only the named `leapview.contract/v1` resource projections are claimed. |
| Compatibility, security impact, immutable publication, and affected dashboards | FAI-622/662/632, PR [#548](https://github.com/flidai/leapview/pull/548) | Unified classifier tests; immutable PostgreSQL publication evidence; replay/conflict/concurrency tests; exact baseline/candidate and direct affected-resource seed; `TestDirectAffectedResourceSeedQualifiesTransitiveDashboardConsumers` | Implemented for immutable publication and the qualified dashboard-consumer profile | Publication retains only the direct seed. The Project graph qualifies sorted, deduplicated direct/transitive dashboard consumers and rejects invalid graphs; no generic all-resource consumer closure is claimed. |
| Protected semantic activation evidence | FAI-649, PR [#575](https://github.com/flidai/leapview/pull/575) | Exact publication/policy evidence in plan identity; approval binding; transaction-bound registry/control and deployed-policy revalidation | Implemented for the qualified ADR-0017 profile | Unsupported consumers and plan shapes remain fail closed under ADR-0017. |
| ODCS 3.1 export/document profile | FAI-623, PR [#550](https://github.com/flidai/leapview/pull/550) | Pinned schema/checksum, independent CLI oracle, mapping/loss reports, security exclusions, adapter isolation tests | Implemented | Source and Model export only. Import, round-trip, execution, and transport are not claimed. |
| OpenLineage 2.0.2 projection/document profile | FAI-629, PR [#551](https://github.com/flidai/leapview/pull/551) | Pinned event/facet schemas; schema/version, quality, statistics, parent and column-lineage tests; single-path/no-transport architecture tests | Implemented | No collector, transport, import, round-trip, or end-to-end emission claim. |
| Generated standards conformance matrix | FAI-632 | [`standards-conformance-profiles.json`](standards-conformance-profiles.json), generated [`standards-conformance-matrix.md`](standards-conformance-matrix.md), `task adr0016-conformance:check` | Implemented | Only implemented or selected capability-gated profiles are registered. |
| Current PostgreSQL migration qualification | FAI-622/662/632 | `TestBaselinePostgreSQL18`, `TestContractPublicationMigrationUpgradesRevisionSixWithRetainedData`, publication constraint/grant checks, repository integrity tests, Goose ordering/immutability and generic previous-version upgrade tests | Qualified for fresh initialization and the exact retained-data 006-to-007 path | The fixture uses the embedded immutable migrations on PostgreSQL 18, preserves a runtime-written Source record, and verifies publication append/replay after 007. It does not claim every historical upgrade origin. |
| Final repository qualification | FAI-632 | Commands and results below, including the clean-worktree `task ci` contract | Qualified | Qualification is green; the separately listed active implementation and evidence boundaries keep ADR-0016 partial. |

## Supported profiles

The generated [standards conformance matrix](standards-conformance-matrix.md)
is the public inventory. It claims only:

- Bitol ODCS 3.1.0, `leapview.dev/odcs-mapping/v1`, export/document;
- OpenLineage 2.0.2, `leapview.dev/openlineage-projection/v1`, export-only
  projection/document.

## Deferred profiles

The matrix records these directions as **Deferred**, with no adapter,
conformance level, validation command, or compliance claim:

- ODCS 3.1 import and round-trip;
- Bitol Open Data Product Standard 1.0.0 export;
- W3C Data Catalog Vocabulary 3 export.

They require a demonstrated capability need and separately reviewed work.

## Validation

Results are recorded only after execution against the FAI-632 branch based on
`origin/main` at `3cdf5df8bd87925af88b230542862fa8c5d4306d`:

| Command | Result |
|---|---|
| `task adr0016-conformance:check` | Pass |
| Contextual Source/Model deprecation regression tests | Pass |
| Direct-seed to transitive dashboard-consumer qualification | Pass |
| PostgreSQL 18 retained-data migration 006-to-007 qualification | Pass; required container gate, no skip |
| Contract projection/version/publication focused suites | Pass |
| `go test ./internal/project/contractodcs -count=1` | Pass |
| `task odcs:oracle` | Pass with the pinned independent `datacontract` 1.1.3 tool prepared on `PATH`; the first local invocation correctly reported the absent CI-only tool |
| `go test ./internal/refresh/openlineage -count=1` | Pass |
| Focused architecture checks | Pass |
| `task test:go:postgres-conformance` | Pass against pinned PostgreSQL 18 container image; no silent skips |
| `task generated:check` | Pass |
| `task docs:check` | Pass after the required clean-worktree generation preparation |
| `task ci` | Pass in a clean detached worktree at the qualification commit; 32m13s |

The first `task ci` attempt in the long-lived development worktree encountered
stale ignored sqlc files left by older branches, and the standalone full
architecture package likewise saw ignored built web assets. Neither artifact
was tracked or reproducible in the clean detached worktree. The clean run
passed SQL generation/audit, APIGen, Go and external-service suites,
PostgreSQL 18 conformance, architecture checks, all frontend shards, and the
generated snapshot gate without changing test requirements.

## Qualification decision

FAI-632 closes the identified contextual field-deprecation, affected-dashboard
graph, and retained-data migration qualification gaps for the implemented
profiles. Publication remains intentionally limited to an immutable direct
seed; transitive dashboard expansion belongs to the Project graph and does not
claim arbitrary consumer kinds. The migration claim is limited to fresh
initialization and the exact embedded revision 006-to-007 path on PostgreSQL 18.
No identified FAI-632 evidence blocker remains; ADR-0016 stays partial until its
final reconciliation records the reviewed completion decision and boundaries.
