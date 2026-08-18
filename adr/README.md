# Architecture decision records

This directory is LeapView's durable architecture decision log. An architecture
decision record (ADR) captures one consequential choice, the context in which it
was made, the alternatives considered, and the consequences the repository must
preserve.

ADRs explain why a decision was made at a point in time. Current architecture
documentation under `docs/articles/architecture/` explains how the implemented
system works. Proposals still under discussion belong in a pull request or an
RFC; implementation progress belongs in Linear.

The log lives outside `docs/` intentionally. Every Markdown file under `docs/`
is a published customer-documentation source and must appear in site navigation.
ADRs are repository records; only implemented architecture belongs in the
customer site.

## Decision log

| ID | Decision | Status | Decision date | Implementation | Amended or superseded by |
|---|---|---|---|---|---|
| [ADR-0001](0001-semantic-model-first.md) | Use a semantic-model-first BI contract | Accepted | 2026-06-18 | Complete | [ADR-0006](0006-adopt-ossie-aligned-semantic-contract.md), authored contract only |
| [ADR-0002](0002-use-maplibre-for-geographic-rendering.md) | Use MapLibre for geographic rendering | Accepted | 2026-07-22 | Complete | — |
| [ADR-0003](0003-retain-narrow-infisical-resolver.md) | Retain the narrow Infisical resolver | Accepted | 2026-07-31 | Complete | — |
| [ADR-0004](0004-defer-incremental-project-reconciliation.md) | Defer incremental project reconciliation | Accepted | 2026-08-05 | Deferred pending corrected measurement | [ADR-0005](0005-use-project-wide-resource-graph.md), scope and identity only |
| [ADR-0005](0005-use-project-wide-resource-graph.md) | Use a project-wide resource graph | Accepted | 2026-08-15 | Complete | — |
| [ADR-0006](0006-adopt-ossie-aligned-semantic-contract.md) | Adopt an OSSIE-aligned typed semantic contract | Accepted | 2026-08-17 | Complete | — |
| [ADR-0007](0007-adopt-plan-driven-project-delivery.md) | Adopt plan-driven project delivery | Accepted | 2026-08-17 | In progress (controlled rollout) | — |
| [ADR-0008](0008-isolate-ducklake-candidate-physical-state.md) | Use one immutable DuckLake catalog per candidate | Accepted | 2026-08-17 | In progress (controlled rollout) | — |
| [ADR-0009](0009-separate-control-and-physical-transactions.md) | Separate control state from immutable physical catalogs | Accepted | 2026-08-17 | In progress (controlled rollout) | — |

## Companion specifications

Mutable implementation and conformance specifications linked by ADRs live
under [`specifications/`](specifications/). They may evolve with schemas, APIs,
tests, and operational tooling while the governing accepted decisions remain
historical records.

- [Project delivery conformance](specifications/project-delivery-conformance.md)

## Conventions

- Copy [`template.md`](template.md) and assign the next unused four-digit ID.
  IDs are permanent and are never reused, including when an ADR is rejected or
  superseded.
- Use a descriptive lowercase filename after the ID. The ID, rather than the
  filename or heading text, is the stable reference.
- Keep decision status separate from implementation progress. Allowed decision
  statuses are `proposed`, `accepted`, `rejected`, `deprecated`, and
  `superseded`.
- Record one cohesive, architecturally significant decision per ADR. Use an ADR
  when a choice is cross-cutting, expensive to reverse, security-sensitive, or
  non-obvious enough that future maintainers will need its rationale.
- Include meaningful rejected alternatives and both positive and negative
  consequences. Avoid using an ADR as a general design specification or task
  checklist.
- Treat the context, decision, alternatives, and consequences of accepted ADRs
  as immutable historical records. Correct spelling and broken links, and update
  coarse implementation or supersession metadata, but record a changed outcome
  in a new ADR and link the old record through `Superseded by`.
- Link related issues, pull requests, specifications, and other ADRs. Linear
  remains the authority for delivery status; do not turn an ADR into a progress
  log.
- Define confirmation evidence. Prefer schemas, architecture checks, tests, or
  operational measurements that make violations observable.
- Update this table in the same pull request that adds or changes ADR status.

The format is intentionally a small subset of
[MADR](https://adr.github.io/madr/). LeapView stores decisions with the code so
the record is reviewed and versioned with the architecture it governs.
