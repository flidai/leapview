# FAI-551 viewport qualification evidence

This directory retains the paired synthetic browser qualification produced by
`task qa:viewport-qualification` on 2026-09-09.

- Measured commit: `3df5af6df8ff56deac70d407daa8983c193be461` (clean),
  rebased onto main `c9e6482a89697ebf6426dd47a369fbd9b83202d9`.
- Report SHA-256: `9b6f5556b66155154256500c092ec74aff01414d6bdd3ec7a76b017d7daed594`.
- Fixture: 24 visuals (8 KPI, 8 Cartesian, 8 table), 1280x820 viewport,
  `600px 0px` root/scroll margin.
- Protocol: one discarded warmup and five measured repetitions per mode,
  alternating order with a fresh browser context per sample.
- Control: current production eager default.
- Candidate: the same generated bundle with `deferMount` enabled before host
  connection by the hashed qualification harness only.

Every eager sample mounted 24 renderers initially; every deferred sample mounted
the 12 hosts within the qualified prefetch band. Forced snapshots mounted all 24,
scrolling caused no disposal or remount, and teardown recorded one disposal per
renderer. Median local observations were:

| Metric | Eager | Deferred |
| --- | ---: | ---: |
| Initial readiness | 1,119.2 ms | 888.1 ms |
| Chromium task duration | 1.058 s | 0.837 s |
| JS heap used | 14,570,476 bytes | 12,926,948 bytes |

These are paired measurements on the exact environment recorded in
`report.json`. They are not production measurements, universal
hardware-independent budgets, or evidence that production callers enable
deferral. Native browser-menu print readiness remains outside this evidence.
