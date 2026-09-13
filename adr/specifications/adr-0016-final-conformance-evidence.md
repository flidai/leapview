# ADR-0016 final conformance evidence

Status: implemented and qualified for the accepted bounded profiles

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

| Requirement | Owner | PR / commit evidence | Validation evidence | Status | Qualified boundary |
|---|---|---|---|---|---|
| Authored analytics/control-plane boundary | FAI-616 | PR [#572](https://github.com/flidai/leapview/pull/572) | Removed-kind discovery/schema tests; dashboard-publication deployment guard; architecture ownership guard | Implemented | Project namespace and ResourceUID lifecycle remain ADR-0018-owned. |
| Generated structural authority and governance metadata | FAI-619/632 | PRs [#472](https://github.com/flidai/leapview/pull/472), [#516](https://github.com/flidai/leapview/pull/516); FAI-632 commit `9654b41d1` | TypeSpec-generated Go/JSON Schema; metadata, stable-check-ID and structural-authority tests; `TestSourceDeprecationContext`; `TestModelDeprecationUsesContextualValidation`; publication replay | Implemented | Source and Model projections reject missing/self replacements, replacement cycles, and `deprecation.since` later than the containing contract version. |
| Canonical Source/Model/SemanticModel projections | FAI-620/662 | PR [#534](https://github.com/flidai/leapview/pull/534) | CAN/SRC/MOD/SEM/SER ledger rows; sealed projection; exclusion manifest; analyzed SQL; typed normalization; RFC 8785 and independent fixtures | Implemented | Only the named `leapview.contract/v1` resource projections are claimed. |
| Compatibility, security impact, immutable publication, and affected dashboards | FAI-622/662/632 | PR [#548](https://github.com/flidai/leapview/pull/548); FAI-632 commit `ee87c819d` | Unified classifier; PostgreSQL publication replay/conflict/concurrency; exact baseline/candidate and direct seed; `TestDirectAffectedResourceSeedQualifiesTransitiveDashboardConsumers` | Implemented and qualified | Publication retains the direct seed; the Project graph qualifies sorted, deduplicated direct/transitive dashboard consumers. No generic all-resource closure is claimed. |
| Protected semantic activation evidence | FAI-649 | PR [#575](https://github.com/flidai/leapview/pull/575) | Exact publication/policy plan identity; approval binding; transaction-bound registry/control and deployed-policy revalidation | Implemented and qualified for the admitted ADR-0017 profile | Unsupported consumers and plan shapes remain fail closed. |
| ODCS 3.1 export/document profile | FAI-623 | PR [#550](https://github.com/flidai/leapview/pull/550), commit `dcfca350e` | Pinned schema/checksum; independent CLI oracle; mapping/loss reports; security exclusions; adapter isolation tests | Implemented and qualified | Source and Model export/document only. Import, round-trip, execution, and transport are not claimed. |
| OpenLineage 2.0.2 projection/document profile | FAI-629 | PR [#551](https://github.com/flidai/leapview/pull/551), commit `e1851bc4d` | Pinned event/facet schemas; schema/version, quality, statistics, parent and column-lineage tests; single-path/no-transport architecture tests | Implemented and qualified | No collector, transport, import, round-trip, or end-to-end emission claim. |
| Generated standards conformance matrix | FAI-632 | FAI-632 commit `626384fa3` | [`standards-conformance-profiles.json`](standards-conformance-profiles.json); generated [`standards-conformance-matrix.md`](standards-conformance-matrix.md); `task adr0016-conformance:check` | Implemented and qualified | Only implemented profiles and explicitly capability-gated deferred profiles are registered. |
| PostgreSQL migration qualification | FAI-622/662/632 | PR [#548](https://github.com/flidai/leapview/pull/548); FAI-632 commit `4d9cf1eaa` | `TestBaselinePostgreSQL18`; `TestContractPublicationMigrationUpgradesRevisionSixWithRetainedData`; constraint/grant, repository integrity, ordering and immutability tests | Qualified | Fresh initialization and the exact retained-data 006-to-007 PostgreSQL 18 path; no claim for every historical origin. |
| Final repository qualification | FAI-632 | FAI-632 commits `626384fa3`, `9654b41d1`, `ee87c819d`, `4d9cf1eaa` | Commands and results below: clean-worktree `task ci` at the qualification base plus required focused gates after each bounded gap closure | Qualified | No unresolved requirement remains inside the accepted bounded profiles. |

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
| `task ci` | Pass in a clean detached worktree at FAI-632 qualification base `626384fa3`; 32m13s. Later bounded commits carry their separately recorded focused validation. |

The first `task ci` attempt in the long-lived development worktree encountered
stale ignored sqlc files left by older branches, and the standalone full
architecture package likewise saw ignored built web assets. Neither artifact
was tracked or reproducible in the clean detached worktree. The clean run
passed SQL generation/audit, APIGen, Go and external-service suites,
PostgreSQL 18 conformance, architecture checks, all frontend shards, and the
generated snapshot gate without changing test requirements.

## Final reconciliation

ADR-0016 is implemented and qualified for the accepted bounded profiles listed
above. Publication remains intentionally limited to an immutable direct seed;
transitive dashboard expansion belongs to the Project graph and does not claim
arbitrary consumer kinds. Migration qualification covers fresh initialization
and the exact embedded revision 006-to-007 path on PostgreSQL 18.

ODCS import/round-trip, Bitol ODPS, and W3C DCAT remain capability-gated and
deferred. ODCS execution/runtime transport, OpenLineage collection or delivery,
generic all-resource consumer closure, and semantic surfaces outside the
ADR-0017 admitted profile are not supported conformance claims. ADR-0018,
ResourceUID, dbt, and recovery work neither block nor contribute to this
completion decision.
