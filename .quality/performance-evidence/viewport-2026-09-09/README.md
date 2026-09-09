# FAI-551 viewport qualification evidence

This directory retains the paired synthetic browser qualification produced by
`task qa:viewport-qualification` on 2026-09-09.

- Measured commit: `38b17ed975320a4917c8d960ac9650d375beeb33` (clean),
  rebased onto main `28551460662a00a22d7b25499313d726dc790414`.
- Report SHA-256: `e4cb0c464ecab18fa9df460bc3a3b3385eced760bafe6110796b80e383e40a5c`.
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
| Initial readiness | 1,050.0 ms | 969.9 ms |
| Chromium task duration | 0.992 s | 0.884 s |
| JS heap used | 14,616,932 bytes | 13,037,228 bytes |

These are paired measurements on the exact environment recorded in
`report.json`. This run observed lower deferred medians and p95s, but an earlier
same-day activation run had mixed timing; the retained result is therefore
evidence of this fixture and lifecycle, not a universal speed claim. These are
not production measurements or hardware-independent budgets. Native
browser-menu print readiness remains outside this evidence because the product
has no native print path.
