# APIGen v0.5.2

APIGen v0.5.2 fixes agent-tool projections over discriminated unions whose variants expose arrays with different item schemas. JSON IR remains at `v4`.

## Fixed

- Common union arrays with incompatible item schemas resolve to their safe unconstrained array container.
- `count_as` works for heterogeneous arrays on direct union responses and through nested `Record<T>` values.
- Generated projected-output schemas retain the array value and integer count without imposing an incorrect item schema.
- Nested item selection remains strict and is rejected unless the union variants expose a compatible common item schema.

Regenerate agent-tool artifacts after upgrading so affected projections use the corrected common container schema.
