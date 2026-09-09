# FAI-551 viewport qualification evidence

This directory retains the paired synthetic browser qualification produced by
`task qa:viewport-qualification` on 2026-09-09.

- Measured commit: `86d4bbad488ed655d5a58f4b8dfa5967ad989976` (clean),
  rebased onto main `c9e6482a89697ebf6426dd47a369fbd9b83202d9`.
- Report SHA-256: `884605c0a477f4947972bf4d4394dc7aea273eb3ba57f71b5dbcaa3f394eab7c`.
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
| Initial readiness | 1,509.3 ms | 1,505.9 ms |
| Chromium task duration | 1.366 s | 1.337 s |
| JS heap used | 15,769,016 bytes | 12,966,672 bytes |

These are paired measurements on the exact environment recorded in
`report.json`. The p95 readiness observation was higher for deferred mounting
(1,690.4 ms versus 1,646.1 ms), so this run does not establish a timing
improvement. It does establish lifecycle behavior and records a lower median
heap observation for this fixture. These are not production measurements or
universal hardware-independent budgets. Native browser-menu print readiness
remains outside this evidence because the product has no native print path.
