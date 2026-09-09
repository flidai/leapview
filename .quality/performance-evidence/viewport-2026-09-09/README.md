# FAI-551 viewport qualification evidence

This directory retains the paired synthetic browser qualification produced by
`task qa:viewport-qualification` on 2026-09-09.

- Measured commit: `6ce02a751e9b47b02a07d7dc570f8377ab0135ee` (clean),
  rebased onto main `c9e6482a89697ebf6426dd47a369fbd9b83202d9`.
- Report SHA-256: `04fc8b5fd952d57b59f436e922321cb73f560c48fa62636c0e2d8b8ee02963ca`.
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
| Initial readiness | 927.4 ms | 843.8 ms |
| Chromium task duration | 0.864 s | 0.783 s |
| JS heap used | 15,313,356 bytes | 12,974,420 bytes |

These are paired measurements on the exact environment recorded in
`report.json`. This run observed lower deferred medians and p95s, but an earlier
same-day activation run had mixed timing; the retained result is therefore
evidence of this fixture and lifecycle, not a universal speed claim. These are
not production measurements or hardware-independent budgets. Native
browser-menu print readiness remains outside this evidence because the product
has no native print path.
