# Manual operator qualification — 28 September 2026

The revised operator passed the disposable lifecycle run recorded in
[operator-qualification-20260928.json](operator-qualification-20260928.json)
and its [transcript](operator-qualification-20260928.log). The JSON binds the
exact tested operator and harness source hashes. The trial harness is from PR
#751 at `9786a0fd09f922e2e5da3f544bf9d2670d3d7019`.

The run used private PID, mount and network namespaces, Docker 29.1.3 with the
containerd overlayfs image store, Kamal 2.12.0, SSH, a local anonymous registry,
Caddy and a disposable 5 GiB ext4 filesystem. Fixture versions share a binary
layer. Ten updates retained exactly current plus distinct prior. Capacity and
recovery-material rejection, foreign-image preservation, rejected deployment
restoration, cleanup-failure maintenance, offline no-op, broken-current offline
rollback, persistent Caddy recreation, protected Compose restoration, engine
restart, and surviving remote work after SSH loss passed.

These are synthetic results. Their measured capacity peaks must not be used as
production capacity qualification. Docker/containerd restart is not a real VPS
reboot. Production admission and the real-image A/B migration remain separate
acceptance gates in [the completion plan](../completion-plan.md).

The 18 Python regression tests, focused Go deployment/config/security/OCI tests,
workflow actionlint and whitespace checks passed. Full local and hosted CI
results are tracked on PR #752; draft-skipped checks are not acceptance evidence.

[Production inventory](production-inventory-20260928.json) was read-only. The
original Compose site remains active, its updater is inactive, and Kamal has no
handover marker. Approximately 32.3 GiB was available on the shared Docker and
containerd backing filesystem. No production handover or cleanup was performed.

Review and final checks precede merge. Two eligible main production images,
real-image capacity measurement, controlled handover/restoration, real host
restart and 24 hours of observation remain required before retiring fallback
PR #748. Automatic VPS activation remains deferred.
