# Merged-main bundle proposal — not applied

The unchanged policy rejects the frontend changes already merged into main.
This is an explicit request to review new exact ceilings, not authorization to
apply them. No FAI-551 viewport implementation is included. There is no proposed
extra headroom and the relative ratchet remains zero.

## Identity and method

- Original source baseline: `e704f88068696c9fe1136a51c22b088663976f31`.
- Attributable prior normal verification: `57b0ae9979d3eb18db5e8f7a921710ae1cd12311`,
  retained in [the prior archive](../frontend-bundle-2026-09-08/README.md).
- Current main: `a1c287f409620172938c03637daad646c6a29c64`, including merged #524,
  #537 and #534. The frontend formatting/date-picker changes came from #524.
- Measured integrated candidate: `13f85d197dc64a926cf1711105ab4ef6d7ee29d4`.
- `git diff --exit-code a1c287f409620172938c03637daad646c6a29c64 13f85d197dc64a926cf1711105ab4ef6d7ee29d4 -- web static package.json bun.lock tsconfig.json`
  passed: those application inputs are exactly main, not viewport work.
- Three sequential `PATH=/home/codex/.bun/bin:$PATH bun run build` invocations,
  each cleaning generated outputs through the production build script, produced
  byte-identical full reports. Dependencies and toolchain were reused; this is
  clean-output reproducibility, not three independent dependency installations.
- Full report: [normal.json](normal.json), SHA-256
  `d44cbc53aa4fc27169aae17a91cd1a37e9c97cc5ddaeeb852b92b31efc273820` for each run.
  Original local captures were `.tmp/frontend-bundle-main-integration-{1,2,3}.json`.
- Environment: local shared `codex-netcup` host, Debian GNU/Linux 13, Linux
  `6.12.105+deb13-amd64`, x86_64, 16 QEMU Virtual CPU version 2.5+ logical CPUs.
  Bun 1.3.14, its Node compatibility version v24.3.0, `bun@1.3.14` package-manager
  pin and lockfile SHA are recorded in the full report. These are byte
  measurements, not local or production runtime-performance claims.
- Source-input digest:
  `25ba475bbddfd6975fd64bf18856c5ff3a9984e42d399ee1b49656a9e6b30912`.

## Proposed exact changes

| Entry | Existing raw / gzip | Measured proposed raw / gzip |
| --- | ---: | ---: |
| chat-page | 6,988,964 / 1,398,220 | 6,998,282 / 1,400,149 |
| dashboard-builder | 5,780,641 / 1,116,846 | 5,789,959 / 1,118,776 |
| dashboard-page | 7,635,051 / 1,529,715 | 7,662,189 / 1,534,847 |
| data-explorer | 7,038,035 / 1,407,525 | 7,047,353 / 1,409,453 |
| Deduplicated aggregate | 15,934,947 / 3,183,940 | 15,962,085 / 3,189,075 |

All other logical-entry byte measurements are unchanged. Aggregate growth is
27,138 raw / 5,135 gzip bytes. The existing checker exited 1 with explicit
absolute-ceiling and relative-ratchet violations. It did not report a pass.

The local proposal was generated, without altering the policy, using:

```sh
bun scripts/frontend_bundle_budget.ts --propose-increase .tmp/frontend-bundle-merged-main-proposal.json --reason 'Review-only proposal for frontend changes already merged in main a1c287f40 (#524); three identical clean builds of integrated controls13f85d197. No FAI-551 viewport code included, no extra headroom, existing policy is unchanged.'
```

Its review flag remains false. Before any application, obtain explicit review
of these changes and regenerate/validate the proposal against a fresh build of
the then-current checkout: proposals bind the measured commit and source digest.
Never edit identity fields to make stale evidence pass. An applied policy still
requires independent collaborator approval of the resulting PR head and all
final gates; approval of this numerical proposal alone does not authorize merge.
