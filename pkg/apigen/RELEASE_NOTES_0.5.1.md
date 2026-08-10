# APIGen v0.5.1

APIGen v0.5.1 hardens the discriminated-union and agent-tool support introduced in v0.5.0. JSON IR remains at `v4`.

## Fixed

- Agent tools accept successful responses that offer JSON alongside binary media. Their output schema is derived from the sole compatible JSON shape while server and CLI generation retain every representation.
- Generated agent-tool requests negotiate the selected JSON representation. An endpoint `Accept` parameter is hidden from model input and bound to the generated JSON media type.
- Tool projections resolve compatible common properties through discriminated unions, including direct objects, array items, and `Record<T>` values.
- Projection requiredness is preserved across union variants; a field is optional unless every variant requires it, and incompatible same-name properties fail with an explicit diagnostic.
- CLI collection and detail configuration resolves common inherited and union properties for envelopes, items, table columns, and quiet fields.
- Raw agent-tool `oneOf` schemas retain inherited required and optional fields in every closed object branch.
- Deterministic property ordering now appends inherited or synthesized properties that are absent from a derived schema's authored order.

Regenerate agent-tool and CLI artifacts after upgrading so generated contracts contain the selected response media type and corrected union schemas.
