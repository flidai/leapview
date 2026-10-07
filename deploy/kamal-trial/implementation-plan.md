# Kamal trial implementation checkpoint

Updated: 28 September 2026.

The earlier staged CI-activation proposal in this file is superseded by the
[public-site completion plan in PR #752](https://github.com/flidai/leapview/blob/ganesh/site-kamal-production/deploy/kamal-site/completion-plan.md).
Automatic VPS activation and new runner access remain deferred. The permanent
path is explicit operator admission, anonymous immutable image pull, supervised
Kamal boot/rollback and bounded current-plus-prior storage.

This PR preserves the reproducible disposable trial harness and its evidence.
The synthetic HTTP fixture has a separate Go module and is not the production
site. Trial package images are not eligible for permanent production deployment.
The temporary real-VPS trial results are recorded in PR #752; the original
Compose/Caddy deployment was restored afterward.

Read-only inventory on 28 September found the original public site healthy,
the updater inactive, approximately 32.3 GiB free, and no permanent Kamal
handover marker. The old full-disk observation is historical, not current state.

The revised operator's disposable qualification is recorded under
[`deploy/kamal-site/evidence` in PR #752](https://github.com/flidai/leapview/tree/ganesh/site-kamal-production/deploy/kamal-site/evidence).
Neither synthetic qualification nor the earlier temporary cutover completes
permanent migration. Required checks and review precede merge; two eligible
production images, real capacity measurements, controlled restoration, Caddy
recreation, a real host restart and 24 hours of observation remain acceptance
gates. Keep fallback #748 uninstalled and available until those gates pass.
