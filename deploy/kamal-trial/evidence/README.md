# Trial results — 26 September 2026

**Not yet qualified for production.** These are real Kamal/SSH/Docker tests with
synthetic application images. They establish deployment mechanics, not real-site
artifact admission or the complete production integration.

| Test | Observed result |
| --- | --- |
| Ten healthy deployments, Docker 29.1.3/containerd | Current + previous site containers; settled storage plateaus. |
| Three unhealthy candidates | Existing site keeps serving. |
| Native prune after those failures, retain=1 | **Fails:** deletes the verified prior version and retains an unhealthy attempt. |
| Remove exact recorded stopped failures, then native prune | Prior version survives. |
| Caddy + private proxy | TLS with trial CA, Host/HTTPS forwarding and www redirect pass. |
| Wrong architecture/service, mismatched/missing record, substituted tag | Rejected before app boot; live version unchanged. |
| Registry-offline rollback | Works with saved prior identity; native rollback otherwise uses the new controller configuration. |
| Real ENOSPC on private ext4 filesystem | Pull fails; current site keeps serving; retry succeeds after removing test filler. |
| Kill controller before boot / after traffic switch | Known owner verified dead; observed actual state; explicit recovery succeeds. |
| Concurrent native command | Activation lock rejects it, but its pull already happened; upstream serialization remains necessary. |
| Cleanup error after activation | Command reports failure while the new version stays live; maintenance retry succeeds. |
| Foreign tag on a service-labeled image | **Fails:** native prune removes the unrelated alias. |
| Repeat deployment of the same version | **Fails:** newest stopped copy displaces the previous distinct rollback version. |

`native.json` and `narrow-cleanup.json` are earlier native-snapshotter baselines.
`overlayfs-failures.json` uses production's Docker version and overlayfs backend,
with a 1536 MiB disposable ext4 filesystem for containerd. Its harness hash
identifies the exact script used. The sampled peak includes deliberate filler;
**do not use it to size production**. Logs remain in the operator workspace.

The extra native findings require two further narrow policies to test: reject
ambiguous foreign aliases before mutation, and make a verified identical-version
request a no-op. They are not claimed implemented by these evidence files.
No production activation or cleanup has occurred.
