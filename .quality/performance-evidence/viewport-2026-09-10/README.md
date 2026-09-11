# Current-main viewport qualification

This is synthetic local browser evidence, not production measurements or a
hardware-independent timing budget. The previous run remains in
[the September 9 archive](../viewport-2026-09-09/README.md).

- Clean measured commit: `ca19217329b53800ef06da161c6f34e3cac836e1`.
- Integrated main: `372039da7e2e698114c279dc63b2010f41c37e93`.
- Command: `task qa:viewport-qualification`.
- Report SHA-256: `cb2744d0cfa285604be5be66653276dddfa8f2351ba70d5a052666286c8ced1d`.
- The report records fixture, source, generated-bundle, lockfile, environment,
  and toolchain identities. All source and bundle hashes match the September 9
  reviewed implementation; the new main changes are backend-only.
- Same 24-visual fixture, 1280x820 viewport, 600px prefetch band, one discarded
  warmup and five measured repetitions per mode, alternating fresh contexts.
- Same local Linux x64/QEMU environment, Bun 1.3.14, Chromium 149.0.7827.55.

All deferred samples mounted the fixed first 12 visuals initially and the other
12 through real scrolling before any capture. Eager samples mounted all 24
initially. Neither mode disposed or remounted on scroll; all 24 snapshots and
teardown disposals were recorded in every sample. Qualification passed.

| Local median | Eager | Deferred |
| --- | ---: | ---: |
| Initial readiness | 1,079.4 ms | 978.2 ms |
| Chromium task duration | 1.032 s | 0.901 s |
| JS heap used | 18,874,020 bytes | 15,827,988 bytes |

These observations do not replace installed-runtime qualification. Native
browser-menu print is not covered; explicit snapshot/capture readiness is.
