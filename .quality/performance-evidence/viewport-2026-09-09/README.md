# FAI-551 viewport qualification evidence

This directory retains the paired synthetic browser qualification produced by
`task qa:viewport-qualification` on 2026-09-09.

- Measured commit: `1cdfb66f477d32832f7fb520e79bea92b37fe98e` (clean).
- Report SHA-256: `85c752dc0f9b7cbe7781f6545f7079bc46cc372c7b20d48b6764b9195ef24ab0`.
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
| Initial readiness | 959.7 ms | 828.2 ms |
| Chromium task duration | 0.913 s | 0.769 s |
| JS heap used | 15,224,644 bytes | 12,924,184 bytes |

These are paired measurements on the exact environment recorded in
`report.json`. They are not production measurements, universal
hardware-independent budgets, or evidence that production callers enable
deferral. Native browser-menu print readiness remains outside this evidence.
