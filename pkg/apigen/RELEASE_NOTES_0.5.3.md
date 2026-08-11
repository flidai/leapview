# APIGen v0.5.3

APIGen v0.5.3 fixes CLI normalization for heterogeneous discriminated-union collections and explicit raw output. JSON IR remains at `v4`.

## Fixed

- CLI collection validation retains every original object item branch when union variants expose arrays with different item schemas.
- Common table and quiet fields are resolved across the preserved item branches without flattening away the closed response union.
- Explicit `raw`, `detail`, and `empty` output modes no longer receive inferred collection columns or pagination.
- Authored pagination remains valid only with collection output, while collection defaults continue to infer pagination when appropriate.

Regenerate CLI artifacts after upgrading so heterogeneous union collections and explicit raw fallbacks use the corrected normalized metadata.
