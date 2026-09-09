# ODCS 3.1 export conformance specification

Status: implemented

Profile: `leapview.dev/odcs-mapping/v1`

Standard: Bitol Open Data Contract Standard 3.1.0

Direction and level: export, document

Last updated: 2026-09-08

Governing decision: [ADR-0016](../0016-adopt-standards-aligned-data-contracts-and-interchange.md)

## Boundary

- **ODX-01:** Export consumes immutable FAI-622 publication evidence and
  strictly decodes only its FAI-620 `leapview.contract/v1` Source or Model
  projection. Runtime, compiler, graph, artifact, and release structures are
  not adapter DTOs.
- **ODX-02:** The adapter emits ODCS `apiVersion: v3.1.0` and validates against
  the upstream schema from tag `v3.1.0`, commit
  `b9d3ffc5aabe9e058afe4469cabe5a218fe9946d`, pinned at SHA-256
  `2cb7dd6fe43344d2233e0406438622681dc3ebadcf8f0d606a15b40c8f6752c0`.
- **ODX-03:** Every emitted field is governed by the reviewed mapping manifest.
  Every export returns deterministic mapping and loss reports naming dropped,
  degraded, and unsupported semantics without copying sensitive values.
- **ODX-04:** The only extension is the closed publication-provenance property
  `leapviewContract`, namespace `leapview.dev/odcs-extension/v1`. Unknown
  members or namespace versions are rejected. Shape changes require a new
  namespace and compatibility review.
- **ODX-05:** Credentials, secrets, connection details, physical locations,
  target bindings, executable SQL/AST, and unsupported quality implementations
  are never serialized. An unsafe mapping rejects export without a partial
  document.
- **ODX-06:** Go validation uses the vendored schema. GitHub CI independently
  validates valid and invalid fixtures with pinned `datacontract-cli` 1.1.3 and
  hash-locked dependencies. Document lint explicitly uses the checksum-checked
  vendored ODCS 3.1.0 schema. The CLI is not linked into production binaries and
  receives no credentials; external definition lookup is disabled.
- **ODX-07:** Source and Model export are supported. SemanticModel is rejected
  with an explicit unsupported loss entry because Apache Ossie remains its
  selected interchange boundary.

## Mapping and loss policy

The normative mapping inventory is
[`mapping.json`](../../internal/project/contractodcs/mapping.json). Source
schema fields, governance, authoritative definitions, nullability, and
freshness thresholds map to ODCS schema properties and SLA properties. Model
fields, selected entity constraints, and the closed native check vocabulary map
to ODCS properties, relationships, and library quality rules where safe.

The initial profile reports these known limitations:

- published evidence maps to ODCS `active`, but publication does not itself
  prove serving activation;
- exact LeapView Decimal maps to ODCS number, which does not preserve the
  exact-versus-approximate distinction;
- schema mode, field deprecation, Model transformation/source bindings, and
  entity semantics without a safe ODCS equivalent are dropped and reported;
- generic ODCS quality arguments for accepted-values and composite uniqueness
  are explicitly marked degraded;
- relationship shorthand must resolve to a property of the exported object;
  missing and external targets reject export because this single-publication
  boundary has no external contract authority.

Consequently the implementation claims only ODCS 3.1.0 **document/export**
conformance. It does not claim import, round-trip, or execution conformance.

## Evidence

| Requirement | Evidence | Status |
|---|---|---|
| ODX-01 | Adapter input type, strict projection decode, and architecture guard | Implemented |
| ODX-02 | Vendored schema, NOTICE, checksum test, valid/invalid schema fixtures | Implemented |
| ODX-03 | Reviewed mapping manifest plus golden mapping/loss reports | Implemented |
| ODX-04 | Sealed extension DTO and unknown-member/version rejection tests | Implemented |
| ODX-05 | Connection, path, ownership-prose, SQL, and target/credential sentinel tests | Implemented |
| ODX-06 | `task odcs:oracle`, CLI 1.1.3 dependency hash lock, valid Source/Model fixtures and unknown-field/version/type rejection fixtures | Implemented / focused qualification passed |
| ODX-07 | Golden Source/Model exports and SemanticModel/Opaque rejection tests | Implemented |

ODCS import, ODCS round-trip, Bitol ODPS, DCAT, and new runtime transports remain
capability-gated and are not implemented by FAI-623.

## Hosted oracle investigation

Canonical hosted [run 34181395170](https://github.com/flidai/leapview/actions/runs/34181395170)
at `4f3bca14c787f4cba5a8afc70c0855d7f008536b` failed before the Go package
suite. Direct CLI 0.9.1 reproduction on 2026-09-08 confirmed:

| Diagnostic | Ownership and disposition |
|---|---|
| SLA `customers.customer_id` unresolved at both thresholds | Upstream-tool limitation: [ODCS 3.1 SLA](https://bitol-io.github.io/open-data-contract-standard/v3.1.0/service-level-agreement/) explicitly permits `Object.Element`; CLI 0.9.1 `validation/structural.rs` checks only object names. Mapping preserved. |
| `instance:acme/source:customers` and Model ID contain invalid characters | Upstream-tool limitation: [ODCS fundamentals](https://bitol-io.github.io/open-data-contract-standard/v3.1.0/fundamentals/) and the vendored schema define a string ID without the CLI's alphanumeric/underscore/hyphen restriction. Existing identity mapping preserved. |
| Model relationship `customers.id` does not resolve | Exporter defect: a shorthand target outside the emitted object lacks an external contract reference. Export now rejects unresolved targets; the golden uses a resolvable local target, with explicit negative tests for missing properties and external targets. |

`go test ./internal/project/contractodcs -count=1` covers the pinned-schema
checksum, strict extensions, golden exports/reports, and relationship rejection.
The added relationship test failed on the old exporter for both missing and
external targets before the correction. The Model golden's publication digest
changes only because its authored test input now names a valid local target;
no digest implementation or existing publication evidence is rewritten.

The former pinned CLI has no schema-only validation switch. With explicit user
approval, it is replaced by [Data Contract CLI 1.1.3](https://github.com/datacontract/datacontract-cli/releases/tag/v1.1.3),
whose `lint --json-schema` command implements the required document-validation
boundary. No diagnostic suppression, weakened schema, or artificial ID/SLA
remapping is used. Positive fixtures remain complete exports, not sanitized
oracle-specific copies. Go tests retain the stricter projection, relationship,
and sealed-extension checks that the independent JSON Schema oracle does not own.

The CI-only dependency input is `scripts/odcs-oracle-requirements.in`; its lock
is generated with `uv 0.12.10 pip compile scripts/odcs-oracle-requirements.in
--python-version 3.12 --generate-hashes --no-annotate --no-header
--output-file scripts/odcs-oracle-requirements.txt` and installed with
`pip --require-hashes`. No connector
extras are installed. The oracle clears inherited environment variables,
disables dotenv/default credential config, and disables external definition
inlining. An ODCS-v3 input guard prevents the CLI's automatic legacy-DCS import
path from accepting malformed ODCS headers; a negative format fixture covers
that boundary in addition to CLI schema rejection of unknown fields, unsupported
v3 versions, and invalid logical types. Both valid exports must pass before each
negative fixture must fail.
The schema checksum is checked before validation. On 2026-09-08,
`task odcs:oracle` passed both valid exports and all four negative fixtures;
`go test ./internal/project/contractodcs -count=1` passed. Canonical hosted CI
results are recorded separately in FAI-648 evidence and must not be inferred
from these focused checks. STR-08, ENF-06, and activation boundaries are unchanged.
