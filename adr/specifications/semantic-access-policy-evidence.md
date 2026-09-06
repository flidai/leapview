# Policy publication evidence — FAI-645

Status: implemented; scoped qualification passed. The wider FAI-645 issue
remains partial.

This is the policy-evidence slice above `a765369eb3353fe9961dbaa2ffca70aa9e3ecf60`
on `ganesh/fai-645-policy-evidence-closure`. It does not activate semantic
access, approve a deployment, remove DataPolicy, or complete FAI-648/632.

## Audited gaps

The FAI-622 classifier already owns structural, semantic and security change
rules and SemVer admission. Its legacy summary class does not retain independent
compatibility dimensions. Unsupported security changes are summarized as mixed,
without explicit indeterminate admission. The publication repository checks
version-transition errors but discards the full classification result, including
`RequiresSecurityApproval`. Generic validation checks do not bind that result
to the baseline, candidate, resource lifecycle or affected-resource explanation.

## Evidence boundary

The existing classifier remains the only rule engine. New publication evidence
retains its independent dimensions and legacy summary, changed paths/domains,
the exact instance-qualified baseline and candidate publication identities,
resource kind, lifecycle history sequence, active bundle and required approval
state. There is no `approved` state at this boundary.

An initial publication is explicitly distinguished from an update. An absent
update baseline, unsupported transition, missing identity, invalid dimension,
stale lifecycle, or missing/inconsistent digest must fail closed. A major
version does not clear a security-approval requirement.

Policy evidence is carried in the existing immutable publication validation
JSON. No historical row or migration is rewritten. Historical validation-only
records remain historical evidence, not automatically qualified policy
decisions. The existing validation envelope remains version 1, as required by
its PostgreSQL constraint; the added nested policy representation carries its
own explicit version. No outer-envelope version upgrade is performed.

Nested policy evidence is bounded to 16 KiB inside the existing 64 KiB
validation envelope; oversized decisions fail closed. Qualify readers and
writers together before relying on this nested version: the parent binary's
generic JSON decoder ignores the new field and therefore cannot preserve its
policy decision on replay. A binary rollback leaves immutable rows intact but
does not retain policy-aware qualification. Historical rows without the field
remain readable by this layer; older writers cannot produce qualified policy
evidence, and missing evidence never becomes approval.

The evidence digest uses the existing OCI SHA-256 content-digest dependency
and the repository's typed compact-JSON evidence convention. Lists are ordered
and validated; raw policy/attribute values are absent. This does not introduce
another canonicalizer or apply RFC 8785 to non-contract evidence. Existing
contract projection, graph, artifact and release bytes/digests are unchanged.

Publication reuses the existing repository transaction and lifecycle/publication
locks. Admission observes the resource ACTIVE state, kind and history sequence.
Restore requires evidence for the new sequence. An exact retry retains its
original baseline and evidence rather than silently using a later publication.

## Affected-resource planning

Persisted evidence explicitly identifies the directly published resource; it
does not claim to contain an exhaustive consumer graph. A separate pure Project
planning projection consumes that validated evidence and the existing immutable
resource graph. It uses graph-owned dependency queries and existing ledger
resource identities to explain direct and dependent authored resources. The
projection remains Project-owned; it does not add a Project-to-Deployment
capability dependency or duplicate the classifier's compatibility explanation.

The later approval consumer receives the policy evidence digest, graph digest,
classification and approval-required state together. This is review input,
not an authorization decision or a new durable approval store. Runtime consumers
that are not authored graph nodes are outside that graph's impact claim.
This pure historical planning view is not proof of current lifecycle or graph
freshness. FAI-649 must bind those live observations at its activation boundary.

## Remaining boundaries

- FAI-648 owns exhaustive cross-consumer/plan-shape qualification and the final
  requirement matrix; this layer supplies evidence, not that acceptance gate.
- FAI-649 owns production verification, activation wiring, actual security
  approval workflow and DataPolicy removal. Neutral protected representative
  planning remains fail-closed, with no fabricated principal or bypass.
- Provider-backed admission and the broader durable query-decision audit scope
  are not manufactured by a contract publication record.
- FAI-632 remains blocked by the overall activation and standards dependency
  chain. No ODCS/OpenLineage capabilities are added here.

## Validation

The following checks were executed on this stacked layer on 2026-09-06:

| Command / evidence | Result and scope |
| --- | --- |
| `go test ./internal/project/contractversion ./internal/project/identityledger ./internal/project/module -run 'Policy\|Classif\|Version' -count=1` | Pass: independent dimensions, indeterminate admission, exact identities, approval preservation, detached views and graph-owned planning. |
| `go test -race ./internal/project/contractversion ./internal/project/identityledger ./internal/project/module -run 'Policy\|Classif\|Version' -count=1` | Pass: focused race qualification. |
| `LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1 go test ./internal/project/identityledger/postgres -count=1 -v` | Pass: full ledger suite, Docker PostgreSQL 18, no skips. |
| `LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1 go test -race ./internal/project/identityledger/postgres -run '^TestContractPublicationConcurrency$' -count=1` | Pass: required live PostgreSQL exact replay and conflicting publication race coverage. |
| `go test -tags=duckdb_arrow ./internal/project/contractprojection ./internal/project/contractodcs ./internal/refresh/openlineage ./internal/analytics/query/... ./internal/analytics/materialize ./internal/analytics/resultidentity ./internal/project/module -count=1` | Pass: projection/exports, semantic planning, consumer/cache lifecycle and Project integration. |
| `go test ./internal/platform/architecture -count=1` | Pass: existing ownership graph and new policy-evidence authority guard. |
| `task generated:check docs:check` | Pass, repeated after the ledger and operational-boundary updates. |
| `task test:go:postgres-conformance` | Pass: all required pinned Docker PostgreSQL 18 gates, no skips. Adds widening approval and lifecycle-evidence fixtures without removing any previous gate. |
| `task ci` | PASS on the normal unchanged retry: exit 0, 10m08s, admin 21/21, site 51/51, broad Go suites and required PostgreSQL conformance. First run stopped after 4m09s: unchanged admin `agent prompt editor seeds edit mode from value attribute` hit Bun's 5,000 ms timeout; the next test failed after the browser closed. |
| `bun run test:admin-page` | Unchanged isolated retry passed 21/21 in 14.40s; the timed-out test passed in 497 ms. |

Review caught and corrected a reverse capability dependency in the planning
projection, malformed generated-contract test fixtures, a shallow-copy replay
fixture, and an empty-slice copying regression that could change evidence JSON
from `[]` to `null`. Regression tests preserve exact evidence and do not relax
assertions. These were implementation/test issues, not baseline failures.

The first CI stop is classified as an intermittent browser/environment
qualification failure, not evidence of a policy-evidence regression: the
frontend/test/configuration files are unchanged from the qualified parent,
and both the same shard in isolation and the normal full CI retry passed.
The exact resource or
browser cause is not established; no timeout or assertion was changed.

The wider FAI-645 issue remains partial: this publication evidence slice is not
the consolidated durable query-decision audit/diagnostics scope or production
activation approval. FAI-648/649/632 statuses are not upgraded by these checks.
