# FAI-551 viewport qualification evidence

This directory retains the paired synthetic browser qualification produced by
`task qa:viewport-qualification` on 2026-09-09.

- Measured commit: `cdc5d9fd585d9289d908c6882122b824c1c02782` (clean),
  rebased onto main `dcfca350e3fab443e635832ffa29ebc5aa25d605`.
- Report SHA-256: `be5d78d39ba08b182b24987a925ad75e36a69816280c2d7166d743578def8135`.
- Fixture: 24 visuals (8 KPI, 8 Cartesian, 8 table), 1280x820 viewport,
  `600px 0px` root/scroll margin.
- Protocol: one discarded warmup and five measured repetitions per mode,
  alternating order with a fresh browser context per sample.
- Control: the same generated bundle with `deferMount` forced off before host
  connection by the hashed qualification harness.
- Candidate: current production dashboard behavior with `defer-mount` enabled.

Every eager sample mounted 24 renderers initially; every deferred sample mounted
the 12 hosts within the qualified prefetch band. Forced snapshots mounted all 24,
scrolling caused no disposal or remount, and teardown recorded one disposal per
renderer. Median local observations were:

| Metric | Eager | Deferred |
| --- | ---: | ---: |
| Initial readiness | 1,062.8 ms | 858.3 ms |
| Chromium task duration | 0.999 s | 0.796 s |
| JS heap used | 19,486,356 bytes | 15,909,848 bytes |

These are paired measurements on the exact environment recorded in
`report.json`. This run observed lower deferred medians and p95s, but an earlier
same-day activation run had mixed timing; the retained result is therefore
evidence of this fixture and lifecycle, not a universal speed claim. These are
not production measurements or hardware-independent budgets. Native
browser-menu print readiness remains outside this evidence because the product
has no native print path.
