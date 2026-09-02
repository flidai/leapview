# ODCS 3.1 export conformance specification

Status: implemented

Profile: `leapview.dev/odcs-mapping/v1`

Standard: Bitol Open Data Contract Standard 3.1.0

Direction and level: export, document

Last updated: 2026-09-02

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
  validates valid and invalid fixtures with pinned `odcs` CLI 0.9.1, whose
  declared upstream specification version must be 3.1.0. The CLI is not linked
  into production binaries and receives no credentials.
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
  are explicitly marked degraded.

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
| ODX-06 | `task odcs:oracle` and stack-tip GitHub CI step pinned to CLI 0.9.1 | Implemented |
| ODX-07 | Golden Source/Model exports and SemanticModel/Opaque rejection tests | Implemented |

ODCS import, ODCS round-trip, Bitol ODPS, DCAT, and new runtime transports remain
capability-gated and are not implemented by FAI-623.
