# Trial results — 26 September 2026

**Not yet qualified for production.** These are isolated Kamal/SSH/Docker tests
with synthetic fixtures and a separately admitted real-site image. They establish
deployment mechanics and compatibility, not the complete production integration.

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
| Foreign-alias guard and verified identical-version no-op | Trial helpers reject ambiguous cleanup and avoid replacing a verified live version. |
| Public acceptance failure after proxy readiness succeeds | Supported `redeploy --skip-push` defers pruning; both verified versions survive. Local rollback and exact rejected/duplicate-container cleanup preserve the distinct prior version. |
| Registry outage while re-pulling the current version | Live and prior containers survive; registry restoration and a subsequent healthy deployment pass. |
| Unrelated image sharing site layers, running/stopped foreign containers | Image, aliases and containers survive site prune; Caddy and the new site keep running. |
| Admitted real-site image | Exact running amd64 manifest, public build identity, HTTPS, www, health/readiness, installation docs, release metadata and 16 CSS/JS assets pass. |

`native.json` and `narrow-cleanup.json` are earlier native-snapshotter baselines.
`overlayfs-failures.json` uses production's Docker version and overlayfs backend,
with a 1536 MiB disposable ext4 filesystem for containerd. It includes the narrow
trial remedies, a verified private SSH working directory and restoration of
the prior host record. The ten settled measurements plateau around 348 MiB after
the second synthetic update. Its sampled peak includes deliberate filler;
**do not use it to size production**. Concurrent garbage collection sometimes
invalidated `du` samples; these sampling errors remain visible in the report.
Logs remain in the operator workspace.

`storage-edges.json` covers deferred acceptance, rollback cleanup, registry outage
and foreign shared layers. Reproduce with `storage_edges.py` and the same isolated
launcher/arguments as `lifecycle.py`, omitting `--mitigate` and `--disk-mib`.

`real-site.json` consumes the successful [trial image run](https://github.com/flidai/leapview/actions/runs/36228605549)
at source `b83767f8a5c6a2d158968e3458295ff50890e4c1`. The admitted index is
`sha256:8e653a743d65a8d90b2bbf65a620524c4d378f936f7754407c0a9fa09d3d8339`.
This is an experimental package, not a production-admitted image. Physical
Docker/containerd allocation increased from 333,488,128 to 894,853,120 bytes
(about 535 MiB) for one host-platform site pull/start. This one-version
measurement is not a production capacity threshold; multi-version update peaks,
host filesystem reserve and garbage-collection settling still need measurement.
`tooling-sha256.json` records the actual fixture, proxy/Caddy archive, registry
binary and gem-lock checksums. Archive checksums identify the local test inputs;
upstream OCI digests in the README identify their source images.

The final lifecycle evidence fixes an earlier harness bug: Kamal ignores a YAML
`run_directory` field, so the SSH wrapper now changes to each fixture's private
HOME before invoking commands. Earlier sequential native baselines establish the
documented cleanup behavior; only the final runs assert metadata isolation.

Still required: a verified hosted-runner SSH route, a production adapter that
enforces these remedies and shared CI/operator serialization, rollback from a
truly separate runner, production reserve sizing, reviewed default-off integration
and controller handover. The host-record rollback test uses SSH but shares the
fixture filesystem with its controller. No production activation or cleanup has
occurred. Full `task ci` remains blocked by the workspace's missing `docker0`
bridge; focused guard, security-policy/admission tests and actionlint passed.
