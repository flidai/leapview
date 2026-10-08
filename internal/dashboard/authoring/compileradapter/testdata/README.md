# Retained Dashboard v1 qualification fixture

`retained-v1-revision.json` freezes an authored revision using the existing v1
contract at dashboard-contract baseline `07b037463a42795dee697a19aec8d2fbc8afe3d5`.
It was captured for qualification on 2026-10-08; it is not a production database
export or a claimed fixture from an older released reader.

The authored document hash was computed once using `DashboardContentHash`:
`sha256:82bfec0cf143f474503705bce510d011aa59428702e2a13ba0773dc7be020310`.
Tests read and verify the literal fixture; they never regenerate its contents or
expected hash. Meaning changes require an explicit compatibility decision before
updating this fixture.

The fixture covers ordered monthly/date and status selections, aliases, sort,
limit, an explicit false presentation option, a neighboring KPI, visual reuse,
page/component order, omitted report defaults, an empty inherited page override,
and an explicit zero gap/padding override with intentional empty rows.
