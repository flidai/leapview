# Profile adoption, observation and retirement

Use this handover record with the existing deployment runbook for the selected
output and installation. Prepare it before changing a production caller or
handing a service to its operator. Keep private inventory, credentials, recovery
locations and raw operational receipts in protected storage; publish sanitized
identities and receipt hashes in the project's delivery ledger.

Implementation owner: Ganesh Kambli. Independent implementation reviewer: Anand
Bora. The service, infrastructure, database and recovery owners must be named in
each installation's record before handover; code review does not assign those
operational responsibilities.

## Select one adoption boundary

| Target | Qualification required before handover | Operating and recovery instructions |
| --- | --- | --- |
| Static CLI or installation bundle | Exact archive hash, source, platform, signed evidence and every advertised host's installation/use/recovery result; Compose bundles also need the selected application's admission and installed lifecycle | [Nix output gates](../nix/README.md#release-adoption-gates), [host installation](host/README.md), [Compose qualification](compose/QUALIFICATION.md) |
| Application image or Linux application archive | Exact final digest/archive, both supported architectures, runtime/Go/native-library evidence, immutable promotion, historical transition and applicable installation/recovery receipts | [Release gates](../nix/README.md#release-adoption-gates), [Compose runbook](compose/README.md) |
| Public Compose | Exact image and package, protected first publication, public dependency boundaries, compatible upgrade, persistent data and offline recovery; include Nix output qualification when adopting a Nix-built replacement | [Compose runbook](compose/README.md), [qualification](compose/QUALIFICATION.md), [host runbook](host/README.md) |
| Public site | Exact site image, platform, capacity/topology and recovery receipts; a Nix replacement needs its own build-output qualification | [Manual site operator](kamal-site/README.md), [observation and acceptance](kamal-site/observation.md) |
| Linux desktop | Exact archive/package hash, native/Electron security and installation/use/upgrade/recovery evidence for the advertised Linux matrix | [Desktop output qualification](../nix/README.md), [desktop runbook](../desktop/README.md) |
| Managed installation | Exact app image and configuration, accepted deployment/credential decisions, protected two-host installation, host/database/app maintenance, credential lifecycle, coordinated recovery, monitoring and owner-approved eligibility/recovery bounds; add Nix output admission for a Nix image | [Managed host runbook](managed/README.md), [application maintenance](managed/maintenance.md), [remaining profile gates](managed/completion-plan.md) |

Outputs and profiles advance independently. A qualified conventional managed
image need not wait for a Nix desktop build. Site acceptance does not admit a new
site image, the stateful application or a managed customer installation. Public
Compose keeps its supported self-hosting path without a managed-service account.
Keep the demo on its existing qualified Compose path until its replacement
lifecycle and installation have their own accepted evidence.

## Prepare the installation record

Record the following in the installation's protected change record and link its
sanitized summary from Linear. Missing information keeps the handover pending.

| Record | Required contents |
| --- | --- |
| Identity | Profile/output, installation identifier, reviewed source revision, immutable image/platform digests or archive hashes, host generations and configuration digest |
| Authority | Implementation review; service owner and eligible customer/workload scope; distinct host, database, app and recovery owners; approved interruption, recovery-time and acknowledged-data-loss bounds |
| Evidence | Exact producer run/attempt and artifact identities, admission and qualification receipt hashes, test dates, affected compatibility matrix, limitations and evidence expiry |
| Recovery material | Distinct verified prior image/archive, original runtime configuration, controller journals, independently retrievable keys, authoritative database/file recovery set, known-good host generations and protected retrieval instructions |
| Recovery access | Verified operator identity and revocation path, host fingerprints, rescue access, offline/registry-outage procedure and alternate access when normal identity or management services fail |
| Observation | Duration, cadence, representative workload, measured thresholds, alert destination/recipient, escalation owner and stop conditions agreed before handover |
| Execution | Operator, start/end time, exact commands/runbook revision, pre/post state, result, interruptions and linked receipts for every attempted change |

Verify that recovery material can be retrieved and belongs to the recorded
installation before the change. A copy existing on the same failed host is not
independent recovery. Record backup/key retrieval tests without copying secret
values into the public record. A NixOS generation rollback does not restore
PostgreSQL or customer files.

## Execute a bounded handover

1. Recheck the selected target's current state, artifact/configuration identity,
   capacity, pending operations, admission and required receipts. Resolve any
   identity drift, failed/stale evidence or uncertain controller ownership first.
2. Perform the existing runbook's start smoke and verify the retained recovery
   material. Obtain the recorded service owner's acceptance of eligibility and
   measured recovery bounds before customer onboarding.
3. Hand over internally first, then advance only the explicitly selected customer
   batch. Use the reviewed profile operator and its durable operation state.
   Preserve separate host, database and application maintenance ownership.
4. Verify the active artifact, public behavior, private boundaries, data and
   accepted acknowledgements. Record measured interruption and manual work.
5. Start the predefined observation. A failed or incomplete change stops the
   batch; use the profile's recovery procedure and retain the failed evidence.
   For managed application operations, inspect/recover the existing operation
   journal before starting another handoff.

Do not turn a development qualification receipt into a production admission
receipt. In particular, `releaseAdmission: false` and
`fullManagedProfileQualified: false` retain their stated limits after a PR merges.

## Observe the adopted profile

For the public site, use the existing fixed 24-hour observer and frozen acceptance
bundle, including both boundary smokes, actual process exits, and the final
retention/recovery audit. Follow its rules for interrupted observations and
production mutations. Do not replace the complete interval with elapsed time or
an isolated successful HTTP probe.

For Compose and managed installations, agree the duration and thresholds for the
actual workload before handover; this document supplies no universal acceptance
duration. Cover exact build/readiness, representative governed reads, writes and
acknowledged uploads, SSE/worker behavior, capacity and inode reserves, backup/WAL
and file-recovery freshness, credential use, actionable off-host alerts, and
recovery access. Preserve sample cadence, gaps, failures and operator responses.
Define which corrective mutations require a new interval. Acceptance records
must identify the actual interval, end smoke, recovery/retention audit and owner
decision. Missing samples or stale monitoring leave acceptance pending.

## Retire only the replaced responsibility

For each builder, script, workflow, controller or fallback proposed for removal,
record:

| Retirement field | Required evidence |
| --- | --- |
| Responsibility | Exact old path and the replacement that now owns the same work |
| Callers | Repository search results plus reviewed external/scheduled/operator callers; confirm all have migrated |
| Compatibility | Accepted replacement behavior and failure/recovery cases for the affected output/profile |
| Recovery | Material and procedures retained independently; explicit treatment of the old path's fallback role |
| Observation | Completed interval and final recovery/retention acceptance for that particular replacement |
| Removal | Reviewed commit/PR, responsible owner, deletion date and post-removal smoke |

Search tracked callers with `rg` before proposing removal, then inspect external
operations inventory. Repository search alone cannot establish that a scheduled
or operator caller is gone. Remove only paths covered by the completed record;
preserve public Compose and supported non-Linux paths that still own a
responsibility. Keep build-output retirement separate from customer migration.

## Existing closeout to reuse

The conventional public site's recorded manual rollout completed its replacement
24-hour observation, boundary smokes and retention/recovery audit on 2 October
2026. [The accepted evidence](kamal-site/evidence/final-acceptance-20261002.md)
landed in [PR #803](https://github.com/flidai/leapview/pull/803), merge
`c7b16569b9e9da1331d1c2fbb27e1bbb91046984`. The separately reviewed fallback
[PR #748](https://github.com/flidai/leapview/pull/748) was closed as superseded.
Reuse that exact historical closeout; automatic site activation remains deferred.
It is not a current live-health claim or acceptance of a Nix site replacement.

The deployment proposal removed by
[PR #922](https://github.com/flidai/leapview/pull/922) remains under independent
decision review. [PR #921](https://github.com/flidai/leapview/pull/921) is
reference-only. This runbook records operational handover requirements and does
not reconstruct that proposal or accept its deployment/credential contract.
