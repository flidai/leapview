# Remaining public-site Kamal migration plan

Updated: 29 September 2026. Implementation resumed after the 03:53 UTC reassessment.

## Execution checkpoint

- Main snapshot (`2a08a13da`) has been merged into both Kamal branches.
- Trial merge `671395792` is pushed; refreshed CI, security, Electron and
  isolated image qualification all passed.
- Production operator forwards the existing GitHub login to live admission and
  supports explicit offline `recover` after an interrupted switch. Review fixes
  reconcile a completed pull and verify Caddy public ports, persistent storage and
  immutable image references. All 39 operator regression tests pass.
- Capacity now requires the exact image digest to be listed in each filesystem
  qualification record; smaller compressed downloads do not bypass measurement.
- Expanded remote lifecycle and actual-topology readiness qualification passed.
  The 29 September evidence binds the final operator source hashes; independent
  review found no remaining recovery-guard defect.
- Local `task ci` passed preparation, generators, Go packages/application shards,
  external integration, PostgreSQL conformance and coverage. It then failed on
  the app-shell Undo-expiry browser test (5-second timeout, followed by closed
  browser errors). Diagnosis and unchanged-test rerun are underway; there is
  still no green aggregate local result. Tests/watchdog limits remain unchanged.
- The expanded interruption run reproduced a recovery cleanup bug: the correct
  site was restored but an extra container remained running. The guarded native
  Kamal stale-container stop is implemented and its regression tests pass. The
  full disposable run passed against the final source, including the reproduced
  interrupted-switch scenario and a lost acceptance response.
- Review, merge, production images, host handover and observation gates remain
  outstanding. No production mutation has been performed in this implementation.

## Latest reassessment

The implementation now integrates main snapshot
`2a08a13da0a309ed6972d17ec6c5839f99a72c4e`, including #764 (Node image),
#765 (Vitest/security evidence), and #766 (GitHub Actions pins), after #762.
The production/runtime rows below describe the read-only 03:53 UTC snapshot;
they are not a claim that later workflow runs have been reassessed.

| Area | Verified status |
| --- | --- |
| #759 | Merged: SQL/API/UI-signal prerequisites now precede image qualification. Preserve this fix when refreshing the Kamal branches. |
| #762 | Merged: environment ordering no longer invalidates an otherwise identical demo rehearsal. |
| Demo deployment | Latest run 36422789400 passed runtime deployment, showcase publication and CFO browser verification. This is separate from the public-site migration. |
| Public-site image | Latest run 36415755679 built, admitted and verified both architectures and the combined image successfully. |
| Public-site activation | The same run failed waiting for the public site to change, then restored the previous production tag. Main still contains the legacy promotion/activation workflow. |
| Live public site | Fresh checks returned `ok` for health and readiness; original Compose/Caddy containers remain running. No website crash was observed. |
| Live source/image | Revision `5d870e7cf5f7e174dce115c416f6cab5e8259dcf`; image `ghcr.io/flidai/leapview-site@sha256:0d2d1ca0017baa3ecda8c3a2c9a75d3d149eef42b3c975d7d4a93702ed48f6c8`. |
| Release metadata | Live site advertises `0.2.0-rc.1`; main expects `0.3.0-alpha.1`. Latest public adoption smoke run 36431566050 failed this exact comparison. |
| Updater and Kamal | Legacy timer disabled/inactive; service inactive. No Kamal proxy or permanent state/handover records exist. |
| Capacity | 34,621,898,752 bytes available (about 32.3 GiB), 2,402,001 available inodes on the shared Docker/containerd filesystem. |
| #752 / #751 | Both open drafts, external review required. Both integrate current main; #751 head `671395792` is pushed with CI/security/Electron/image qualification passing. #752 implementation/requalification is ongoing locally. |
| #748 | Still open as fallback; keep it uninstalled. |

The deployment failure is consistent with the deliberately disabled updater:
publishing a new production tag cannot make the running site change. Do not
re-enable the updater to make the obsolete activation job green. Replace that
workflow through reviewed #752, then perform the controlled manual handover.

The latest main site image is not an accepted migration image A: the complete
production workflow failed, and current main's site image lacks the service
ownership label required by the manual operator. Rebuild and re-admit after the
Kamal integration merges. The 24-hour post-migration observation has not started.

## Goal in plain language

Move `leapview.dev` to a deployment process where an operator can install a
verified website version, safely return to the previous version, and avoid
filling the server with old images. Caddy continues to provide public HTTPS;
Kamal manages the website containers and switches traffic between versions.
Automatic VPS deployment remains disabled.

## What is already done

- The manual operator supports `status`, `prepare`, `deploy`, `rollback`, and
  `maintain`, plus explicit interrupted-operation `recover`. It verifies images,
  prevents overlapping operations, checks free
  storage, preserves recovery material, and retains current plus previous images.
- Persistent Caddy-only configuration and the migration/recovery runbook exist.
- Disposable tests passed ten updates, offline rollback with a broken current
  container, failure recovery, storage guards, Caddy recreation, engine restart,
  and loss of the SSH lock connection while remote work continues. The expanded
  run also passed real interrupted pulls/switches and a lost acceptance response.
- All 39 current operator regression tests passed. Earlier hosted CI, security,
  and Electron checks passed on previous heads; refreshed final checks are being
  collected. The earlier reports-lane watchdog issue was followed by a fresh Tini-based
  aggregate run that reached frontend tests, then failed on the app-shell
  Undo-expiry test:
  **there is no green monolithic local `task ci` result**.
- PR #752 contains the implementation and evidence. PR #751 contains the trial
  harness and corrected status/docs. Both remain drafts with review required.

The production inventory above was refreshed read-only on 29 September. Implementation is changing the operator and qualification tooling;
production remains unchanged. Refresh inventory again
immediately before migration; healthy responses alone do not prove a deployment
or that the release/download metadata is current.

## Flow

```text
DONE: Build tooling → Initial disposable tests → Initial hosted checks
      → Refresh both branches against current main

DONE: Fix reviewed recovery/readiness gaps → Expanded lifecycle proof

NOW:  Complete local and refreshed hosted checks

NEXT: External review and resolve feedback
      → Merge #752 and qualify real image A
      → Merge #751 and qualify real image B
      → Measure real-image storage and rehearse recovery
      → Move the live site to Kamal A, proving Compose restoration
      → Deploy B → Roll back to A → Restore B
      → Recreate Caddy and restart the real server
      → Keep only B + A → Observe for 24 hours → Retire fallback #748
```

Images A and B mean two different, successfully qualified production builds.
They are not trial images or mutable version tags.

## 1. Complete review and pre-merge validation

- [x] Integrate current main into the Kamal work before final validation/review.
  Preserve #759's `ui-signals:generate` prerequisite in both image qualification
  entrypoints and the later main changes. Do not treat checks on the old PR heads
  as proof that this refreshed integration passes.
- [ ] Obtain Jacob's review of [#752](https://github.com/flidai/leapview/pull/752)
  and [#751](https://github.com/flidai/leapview/pull/751); address feedback.
- [x] Compare saved evidence with every scenario in the accepted completion
  plan. Complete any missing failure-injection evidence, especially interrupted
  pull/switch and lost acceptance responses; distinguish unit coverage from
  an exercised remote lifecycle.
- [ ] Resolve the local aggregate CI limitation in a suitable environment and
  obtain a passing bounded run. If an exception is proposed, record it explicitly
  through repository review; separate passing commands do not erase the timeout.
- [ ] After integration or other code changes, rerun affected qualification and
  final required checks, keeping source hashes current.
- [ ] Mark the PRs ready when their pre-merge evidence is complete. Satisfy review,
  branch protection, and merge-queue requirements before merging.

Exit: reviewed changes with accepted validation and no unresolved pre-merge gap.

## 2. Produce two eligible production versions

- [ ] Drain old production deployment workflow runs so old automation cannot
  activate an image after the new workflow is merged.
- [ ] Merge #752 with automatic VPS activation absent.
- [ ] Verify the merged workflow publishes/qualifies images without promoting
  the legacy production tag or waiting for automatic VPS activation. Verify the
  rebuilt image carries the operator's required service ownership label.
- [ ] Wait for a successful main production-image workflow, then run live
  `prepare` for image A. Save its immutable image/platform/config identities,
  source revision, qualification evidence, and version-specific release metadata.
- [ ] Merge reviewed #751 and repeat for a distinct production image B.
- [ ] If main advances, use eligible successful main builds. Never select an
  incomplete build to preserve the intended PR sequence.

Exit: two protected production records passing live admission and carrying the
required service label. Trial images and edited JSON claims are not sufficient.

## 3. Qualify real storage needs and prepare recovery

- [ ] Exercise A/B in a disposable environment matching production. Measure peak
  incremental bytes and inodes on each distinct Docker/containerd filesystem.
- [ ] Record exact measured image digests in each filesystem's `qualified_images`,
  and retain their byte/inode measurement reports. Every new digest needs renewed
  disposable measurement, even if its compressed download is smaller.
- [ ] Record the tested compressed-image-size envelope and margins separately:
  candidate headroom at least 1.5 times peak bytes; free-space reserve at least
  the greater of 2 GiB or 10% of capacity; inode reserve at least the greater of
  10,000 or twice peak inodes. Do not reuse synthetic fixture measurements.
- [ ] Refresh production health, exact running image, disk/inode availability,
  Docker/containerd paths, proxy state, and updater state.
- [ ] Save protected original Compose, environment, Caddy configuration and exact
  image identity. Verify the restoration commands and retain required images.
- [ ] Establish exclusive ownership and confirm old work has settled. Resolve
  uncertain remote work explicitly before any new mutation.

Exit: sufficient measured capacity and a verified path back to the original site.

## 4. Perform the controlled live handover

- [ ] Bootstrap the pinned private Kamal proxy and image A; verify A privately
  while keeping the original application available for recovery.
- [ ] Install the persistent Caddy-only Compose configuration with its declared
  external Kamal network, preserving certificates, volumes and public ports.
- [ ] Switch Caddy to Kamal using a timed restoration safeguard. Measure and
  report any interruption; do not promise zero downtime.
- [ ] Verify HTTPS, health/readiness, actual container and build identity, docs,
  release metadata, download links, CSS/JS assets and the `www` redirect.
- [ ] Rerun the public installation smoke check against the intended release
  metadata. Resolve the currently observed `0.2.0-rc.1` versus `0.3.0-alpha.1`
  mismatch by deploying the verified intended version, not by weakening the
  comparison. If main advances, record the exact source/release being accepted.
- [ ] Demonstrate restoration to protected original Compose, then return to A
  and repeat verification.
- [ ] Write permanent handover state only after successful verification. Keep
  legacy updater entrypoints guarded and the updater disabled.

Exit: A serves publicly through persistent Caddy/Kamal, with restoration proven.

## 5. Prove routine deployment, rollback and restart recovery

- [ ] Deploy B using the completed operator command and verify public acceptance.
- [ ] From a fresh operator session, block registry access and roll back locally
  to A using retained recovery material. Verify A, then restore B.
- [ ] Confirm broken-current rollback and interrupted-operation recovery evidence
  covers the final tooling; preserve fail-closed behavior for uncertain work.
- [ ] Recreate Caddy from the active Caddy-only Compose definition. Confirm HTTPS
  returns and the legacy application is not recreated.
- [ ] Perform the controlled real host restart and verify application/proxy
  identity, restart policies, network, public checks and updater inactivity.
  A disposable Docker restart does not satisfy this step.

Exit: production deployment, local rollback, Caddy recreation and host restart
all have recorded successful results.

## 6. Settle storage and observe

- [ ] Verify native pruning leaves B plus distinct verified A. Remove migration-only
  application containers/image references only after recovery acceptance.
  Preserve unrelated resources, Caddy data and protected audit/recovery records.
- [ ] Record final digests, runtime versions, capacity margins, retained images,
  commands and recovery instructions; make the runbook match the live server.
- [ ] Observe website health and disk/inode usage for 24 hours. Record the start,
  end and observations. Postpone closure for unresolved deployment or maintenance
  failures; investigate regressions before declaring acceptance.
- [ ] Close fallback [#748](https://github.com/flidai/leapview/pull/748) as
  superseded only after acceptance, linking replacement PRs and evidence.

Exit: permanent manual migration accepted, storage bounded and observation complete.

## Scope and stop conditions

Keep automatic VPS activation disabled. Do not install #748 alongside Kamal,
change DNS, expand disks, change the CFO demo, or introduce registry credentials
or CI network identities as part of this plan.

Stop before pulling or switching if admission, ownership, recovery material or
capacity is uncertain. Do not clear ownership merely because SSH disconnected.
A rejected candidate restores the verified working version; cleanup failure
after acceptance leaves the accepted version serving and requires explicit
maintenance recovery before another deployment.

## References

- [Accepted detailed completion plan](deploy/kamal-site/completion-plan.md)
- [Operator and recovery runbook](deploy/kamal-site/README.md)
- [Saved disposable qualification and production inventory](deploy/kamal-site/evidence/README.md)
- [Production hosted CI](https://github.com/flidai/leapview/actions/runs/36393816085)
- [Trial hosted CI](https://github.com/flidai/leapview/actions/runs/36394709478)
- [Latest merged deployment fix #762](https://github.com/flidai/leapview/pull/762)
- [Image qualification prerequisite fix #759](https://github.com/flidai/leapview/pull/759)
- [Latest successful demo deployment](https://github.com/flidai/leapview/actions/runs/36422789400)
- [Public-site build success and activation failure](https://github.com/flidai/leapview/actions/runs/36415755679)
- [Public installation smoke failure](https://github.com/flidai/leapview/actions/runs/36431566050)
