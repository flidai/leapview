# Live public-site Kamal rollout — 30 September 2026

The public website is serving through the manually managed Kamal deployment.
The rollout exercised both admitted images, restored the original Compose site,
completed an offline rollback, recreated Caddy, and accepted the application after
a real host reboot. The required 24-hour observation is still running, so final
acceptance and retirement of fallback PR #748 remain pending.

The linked [sanitized machine-readable receipt](live-rollout-20260930.json)
records the live image identities and source revisions, capacity margins, UTC and
IST event times, observer snapshot, and hashes of protected input receipts. It
contains no raw logs or protected locations.

| Role | Immutable site image | Service source revision | Live state |
| --- | --- | --- | --- |
| A | `ghcr.io/flidai/leapview-site@sha256:87a5b2068742b8c216dbfbcc578d93789e4f6db1ca771f44834f654b54de6eec` | `aa823506bf7916f051083cab56dd015cbcadac0d` | Verified prior and offline rollback target |
| B | `ghcr.io/flidai/leapview-site@sha256:acc225d4526738b4a7b55609b1a643e31fdbc034dca305f17ca952a973f16e87` | `0f25215ceb1cc7331701d2f3120da47b66a88714` | Active at the evidence snapshot |

Both images passed the real-image capacity qualification. Docker and containerd
share one qualified filesystem. The measured full-lifecycle peak was
1,269,440,512 bytes and 3,787 inodes. The policy requires 1,904,160,768 bytes of
candidate headroom, a 3,996,463,514-byte reserve, and 10,000 free inodes beyond
the measured peak. That gives minimum pre-pull gates of 5,900,624,282 free bytes
and 13,787 free inodes. Qualification observed 33,906,552,832 free bytes and
2,398,341 free inodes. The largest compressed A/B candidate was 264,041,238
bytes. These figures describe the recorded A/B qualification and its mapped live
capacity policy; they are not synthetic fixture peaks.

The protected original Compose application was restored and publicly accepted
at 07:34:25 UTC (13:04:25 IST). The accepted restore receipt confirms that no
Kamal ready or state marker was written during that restoration. A's successful
bootstrap result records acceptance before permanent state was written; the
result file was written at 08:48:39 UTC (14:18:39 IST), which is its filesystem
receipt time because the JSON does not contain an event timestamp.

B deployment, offline rollback to cached A, and return to B completed at
08:52:57 UTC (14:22:57 IST). The final operator state records B active, A prior,
no pending operation, and no pending maintenance. Caddy recreation and public
acceptance ran from 08:53:56 to 08:54:15 UTC (14:23:56–14:24:15 IST), with the
deployment state unchanged. A real host reboot then recovered B; post-boot host
and public acceptance completed at 08:56:31 UTC (14:26:31 IST). Docker and
containerd were active, the legacy timer remained disabled, and public health,
readiness, build, release, documentation, assets, and redirect checks passed.
The corrected desktop release state was withdrawn with zero advertised
installer links.

Public endpoint sampling during the cutover and reboot window, 08:46–08:56 UTC
(14:16–14:26 IST), recorded 25 failed endpoint samples across 13 sampled seconds.
These point samples do not establish a continuous outage duration; this evidence
does not claim zero downtime. Other unaccepted attempts are excluded from the
successful proof counts.

Observer v3 failed at 09:54:44 UTC during a 20.873-second sample. All three
endpoint checks returned HTTP 200; the failure cause is unknown, and that run is
excluded from the 24-hour acceptance. Observer v4 started at 10:28:20 UTC
(15:58:20 IST). As of its latest recorded sample at 11:17:20 UTC
(16:47:20 IST), it was running with 50 public samples and four host samples;
none of those public samples had recorded failures, and the latest build,
health, and readiness checks each returned HTTP 200. The 24-hour window is due
to finish at 10:28:20 UTC on 1 October (15:58:20 IST). Its separate acceptance
job is scheduled for 10:31 UTC (16:01 IST).

The frozen observer preflight passed with start-smoke evidence bound by hash.
The observer itself does not write the end smoke or final acceptance receipt.
Those, the post-observation image-retention audit, completion of the full
24-hour interval, and closure of PR #748 remain outstanding. Automatic VPS
activation remains disabled.

The JSON receipt indexes protected inputs by SHA-256; raw receipts stay in
protected operator storage and are not copied into the repository.
