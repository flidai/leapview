# APIGen v0.4.1

APIGen v0.4.1 fixes agent-tool JSON Schema emission for unconstrained TypeSpec values.

## Fixed

- TypeSpec `unknown` values now emit as the unconstrained JSON Schema `{}` in agent-tool input and output schemas.
- Nested `unknown[]` items no longer emit the invalid schema `{"type":""}`.
- `Record<unknown>` values now emit `additionalProperties: {}` without an empty `type` keyword.
- Typed scalar, object, array, enum, and recursive-reference schemas retain their existing behavior.

## Compatibility

This is a backward-compatible patch release. JSON IR remains at schema version `v3`; no TypeSpec or runtime API changes are required. Consumers that embed generated agent-tool contracts should regenerate them after upgrading.

## Verification

Release verification covers unconstrained schemas in request and response contracts, nested array items, map values, typed siblings, recursive references, the complete Go test suite, TypeSpec tests and type checking, distribution drift, and example generation smoke tests.
