# FAI-551 viewport qualification evidence

This directory retains the paired synthetic browser qualification produced by
`task qa:viewport-qualification` on 2026-09-09.

- Measured commit: `ba8fbf8ff94ce020d6e7878c814829b63a8fa972` (clean),
  rebased onto main `dcfca350e3fab443e635832ffa29ebc5aa25d605`.
- Report SHA-256: `d8df5cd455d3e2f1bb89fb2e631fef7db713c72d67c2058d155fb359a6181ade`.
- Fixture: 24 visuals (8 KPI, 8 Cartesian, 8 table), 1280x820 viewport,
  `600px 0px` root/scroll margin.
- Protocol: one discarded warmup and five measured repetitions per mode,
  alternating order with a fresh browser context per sample.
- Control: the same generated bundle with `deferMount` forced off before host
  connection by the hashed qualification harness.
- Candidate: current production dashboard behavior with `defer-mount` enabled.

Every eager sample mounted 24 renderers initially; every deferred sample mounted
the 12 hosts within the qualified prefetch band. Real scrolling mounted the
remaining 12 before any snapshot call, and the report records these new mounts
separately from initial mounts. Scrolling caused no disposal or remount;
subsequent snapshots covered all 24, and teardown recorded one disposal per
renderer. The browser tests separately prove that snapshot forces an offscreen
host to mount without an intersection callback. Median local observations were:

| Metric | Eager | Deferred |
| --- | ---: | ---: |
| Initial readiness | 1,081.0 ms | 975.7 ms |
| Chromium task duration | 1.024 s | 0.894 s |
| JS heap used | 18,850,060 bytes | 15,649,124 bytes |

These are paired measurements on the exact environment recorded in
`report.json`. This run observed lower deferred medians and p95s, but an earlier
same-day activation run had mixed timing; the retained result is therefore
evidence of this fixture and lifecycle, not a universal speed claim. These are
not production measurements or hardware-independent budgets. Native
browser-menu print readiness remains outside this evidence because the product
has no native print path.
