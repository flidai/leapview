# FAI-551 viewport qualification evidence

This directory retains the paired synthetic browser qualification produced by
`task qa:viewport-qualification` on 2026-09-09.

- Measured commit: `a9b1ead78642bce69da2109f3c53a1bdc3fe5226` (clean),
  rebased onto main `dcfca350e3fab443e635832ffa29ebc5aa25d605`.
- Report SHA-256: `ba7ede8fc50ec5bcb0d7ab7c73187fa0eebc2a10b0561ef31e64e0981d44c067`.
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
| Initial readiness | 1,400.7 ms | 1,107.2 ms |
| Chromium task duration | 1.323 s | 1.004 s |
| JS heap used | 19,590,316 bytes | 15,860,540 bytes |

These are paired measurements on the exact environment recorded in
`report.json`. This run observed lower deferred medians and p95s, but an earlier
same-day activation run had mixed timing; the retained result is therefore
evidence of this fixture and lifecycle, not a universal speed claim. These are
not production measurements or hardware-independent budgets. Native
browser-menu print readiness remains outside this evidence because the product
has no native print path.
