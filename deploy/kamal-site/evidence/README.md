# Manual operator qualification — 29 September 2026

The final operator passed the disposable lifecycle run recorded in
[operator-qualification-20260929.json](operator-qualification-20260929.json)
and its [transcript](operator-qualification-20260929.log). The JSON binds the
exact tested operator and harness source hashes. All recorded operator hashes
match the committed source. The trial harness is from PR #751 at
`671395792d42b26a0d9cc38f36bb133ecabf82b1`; both branches integrate main snapshot
`2a08a13da0a309ed6972d17ec6c5839f99a72c4e`.

The run used private PID, mount and network namespaces, Docker 29.1.3 with the
containerd overlayfs image store, Kamal 2.12.0, SSH, a local anonymous registry,
Caddy and a disposable 5 GiB ext4 filesystem. Fixture versions share a binary
layer. Ten updates retained exactly current plus distinct prior. Capacity and
recovery-material rejection, foreign-image preservation, rejected deployment
restoration, cleanup-failure maintenance, offline no-op, broken-current offline
rollback, persistent Caddy recreation, protected Compose restoration, engine
restart, and surviving remote work after SSH loss passed.

The expanded remote failure matrix also passed:

- An interrupted real digest pull retained pending state and blocked takeover
  until remote work settled and ownership was audited. Recovery used local
  retained content with the registry offline.
- Terminating the actual Kamal client after the proxy switched left the saved
  active plus pending candidate recorded. Explicit recovery restored the saved
  version and stopped validated stale containers before clearing pending state.
  This scenario reproduced an extra-live-container cleanup failure before the
  recovery fix; the final run verifies the corrected behavior.
- A host acceptance committed before its RPC response was discarded. The
  unresolved owner prevented takeover; after audit, maintenance preserved the
  accepted version instead of guessing a rollback.

Production `load_ready`/topology checks passed against actual disposable Docker
containers twice, after Caddy recreation and after engine restart. The report
lists the exact fixture bindings (repository, filesystem paths and inactive
systemd state); runtime image, ports, mounts, network, restart policies and
configuration-drift checks were exercised. The proxy archive preserves the
pinned OCI digest.

These are synthetic results. Their capacity peaks must not be used as production
capacity qualification. Every real image digest needs measured byte/inode
qualification on matching storage/runtime. Docker/containerd restart is not a
real VPS reboot. Production admission and the real-image A/B migration remain
separate acceptance gates in [the completion plan](../completion-plan.md).

All [39 Python operator regression tests](operator-tests-20260929.log) passed
against operator commit `21907af07` using `python3 -m unittest discover -v -s
deploy/kamal-site -p 'test_*.py'`. Focused Go deployment/config/
security/OCI tests, actionlint and whitespace checks passed. Final aggregate
local and exact-head hosted CI results are tracked on PR #752 and in
[the remaining plan](../../../plan.md); draft-skipped checks are not evidence.
The [28 September report](operator-qualification-20260928.json) and
[transcript](operator-qualification-20260928.log) remain historical evidence for
their recorded source hashes.

[Production inventory](production-inventory-20260928.json) was read-only. The
29 September 03:53 UTC reassessment in the remaining plan confirmed the original
Compose site, inactive updater and absence of permanent Kamal handover records.
Approximately 32.3 GiB was available on the shared Docker/containerd filesystem.
No production handover or cleanup was performed by this implementation.

Review and final checks precede merge. Two eligible main production images,
real-image capacity measurement, controlled handover/restoration, real host
restart and 24 hours of observation remain required before retiring fallback
PR #748. Automatic VPS activation remains deferred.
