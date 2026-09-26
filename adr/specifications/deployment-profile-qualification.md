# Deployment profile qualification

Status: proposed requirements; no profile is qualified by this document

Date: 2026-09-25

Last revised: 2026-09-26

Related: [ADR-0025](../0025-share-an-open-deployment-stack-for-self-hosted-and-managed-leapview.md),
[supporting technology research](deployment-stack-reuse-research.md)

## Purpose and release gates

Qualify public Compose self-hosting and the operated Kamal profile using existing
tooling, shared LeapView lifecycle commands, and the UBDR occurrence/evidence
contracts. Linear tracks owners and delivery status. No production capability or
numeric service guarantee is established by this specification.

| Gate | Required before | Open decisions and evidence |
|---|---|---|
| Operated VPS, PostgreSQL and local analytical rebuild | Managed launch | Provider/region selection, filesystem qualification, rebuild time, protected customer state, isolation and eligibility |
| Bundled Compose dependencies | Advertising bundled single-host production support | Local persistence, rebuild/publication and PostgreSQL dependency lifecycle evidence |

Record the release, configuration, providers, resource layout, reviewer and dated
results for each gate. The managed profile uses one application VPS per customer
with local SSD and external managed PostgreSQL. Compose uses local persistent
storage and bundled PostgreSQL by default. Neither profile requires S3 for
analytical serving. Managed operations additionally require monitoring and off-host
protection of irreplaceable state; self-hosters configure those integrations.
Analytical output backups are optional when full reconstruction is qualified.

## Operated profile qualification

### Ownership and locations

Record European application, database, object and backup regions, plus telemetry,
secret-service and support access locations. Name the accountable party,
implementation, maintenance process, escalation and recovery evidence for each:

| Responsibility | Required coverage |
|---|---|
| Hetzner resources | Account ownership, dedicated customer VPS, quotas, replacement capacity and region failure |
| Host and Docker | OS baseline, vulnerability updates, Docker upgrades, reboot windows, firewall and recovery |
| Kamal and proxy | Pinned versions, application updates, rollback, HTTPS renewal, trusted client identity and SSE |
| Managed PostgreSQL | Customer isolation, roles, TLS, HA/failover, major upgrades, PITR, retention, export and restore |
| Local SSD | Persistent mount layout, isolated paths, integrity, capacity, rebuild/publication and safe cleanup |
| Off-host recovery destination | Protection of customer state/retained inputs, access isolation, retention, deletion protection, restore/export and keys |
| Operations | Scoped credentials, monitoring, retained audit records, support coverage, incident response and supplier exit |

OpenTofu provisions declared infrastructure; Ansible configures hosts; Kamal owns
application/proxy deployment. Do not let Compose and Kamal manage the same
containers. Managed data providers own only the duties explicitly supported by
their service and contract. LeapView retains coordinated recovery correctness.

### Isolation and durable state

Use one customer application VPS and scoped deployment credentials per customer.
Specify dedicated database resources/roles and isolated filesystem paths,
optional bucket resources/credentials, backup access and shared administration.
Do not infer physical host or storage hardware exclusivity. Reject cross-customer
database, file, object and operator access. Document shared provider dependencies.

Classify every persisted input/output before admitting rebuild-based recovery:

| Class | Examples | Recovery requirement |
|---|---|---|
| Authoritative application state | Permissions, configuration, customer edits, required operational records | Preserve and restore; analytical rebuild cannot replace it |
| Retained source inputs | Uploads without another guaranteed copy, non-replayable incremental history | Preserve independently of derived analytical files |
| Reconstructible analytical outputs | Materializations from fully rereadable sources | Full extraction, transformation, validation and publication within agreed bounds |
| Disposable runtime data | Query result caches, intermediate files, spill | Recompute; do not restore as authority |
| DuckLake metadata | Snapshot/file references for a physical pool | Recover consistently with files or initialize a fresh catalog and rebuild |

Verify retained source definitions, credentials, extraction completeness, rate
limits, source retention and rebuild cost. Incremental pipelines must support a
full rebuild or their irreplaceable input history must be protected. Source
availability is a recovery dependency. Regeneration produces new source data;
it cannot claim exact recovery of an old snapshot without replayable inputs.

### Analytical reconstruction protocol

Use normal snapshots for routine refresh in a healthy catalog. For corrupted
metadata or lost analytical files, qualify the following explicit recovery:

1. Record a durable rebuild operation using existing PostgreSQL/River machinery.
   Fence affected writers, block known-corrupt reads and report rebuilding state.
2. Allocate a fresh physical-pool identity, DuckLake metadata schema in PostgreSQL
   and dedicated local directory. Initialize with supported DuckLake APIs and the
   authorized maintenance identity. Keep application control state intact.
3. Rebuild the complete dependency graph from retained inputs. Reinitialize
   extraction checkpoints for the new operation so incremental progress cannot
   skip required data. Avoid upstream side effects during replay.
4. Validate schemas, required data checks and representative governed queries.
   Record source freshness and completed catalog/snapshot identity.
5. Publish through the existing serving-generation activation mechanism. Retry or
   reconcile lost acknowledgements against the durable operation; partial output
   cannot become active. No cross-store atomic transaction is assumed.
6. Retire the old pool after writer fencing and reader/reference release. Use
   supported cleanup operations and maintenance permissions; never manually patch
   DuckLake's internal metadata tables to hide missing files.

Retain healthy old data during a normal rebuild. If the old data is corrupt,
serving remains unavailable for the affected scope until validation succeeds.
Bind leases, caches and serving references to pool/catalog identity plus snapshot;
snapshot numbers reused by a new catalog cannot reuse old results or authority.
Test process restart at every boundary, failed validation, publication races,
return of the previous host and safe cleanup after failed candidates.

Use persistent mounts with the same qualified path mapping across Kamal versions.
Budget current data, candidate output, build/query spill and cleanup headroom.
Exercise disk full, missing files, corruption and source outages. Bound historical
retention by actual reader, rollback and recovery needs; indefinite history is
not a product requirement. Corruption recovery must not activate an incompatible
application/data pair during restart-based application rollback.

### Authoritative state and optional analytical backups

Qualify managed PostgreSQL versions, privileges, TLS, HA/failover, major upgrades,
PITR and backup export. Verify the selected backup policy; managed service status
alone does not establish recoverability. Application-state restoration is distinct
from analytical reconstruction and must preserve authorization and replay safety.

For managed customers, require off-host recoverable copies of customer state,
retained uploads, necessary authored artifacts and protected configuration/keys.
Choose an existing backup service, managed S3 or another qualified destination.
Verify access separation, retention, deletion behavior and restore testing. A
backup on the same VPS is insufficient for host-loss recovery. State the accepted
RPO, including upload acknowledgement versus durable-copy completion.

Backups of fully rebuildable analytical outputs are optional. If used, qualify a
consistent catalog/file recovery point including cleanup and retention behavior.
Managed PostgreSQL PITR alone does not recover local Parquet files. If not used,
prove source reconstruction fits recovery commitments and document upstream-outage
behavior. Do not restore older control state merely because analytical SSD data
was lost. Use native provider operations or established backup tools.

Self-hosted startup requires no off-host storage, monitoring service or backup
account. Ship public backup/export/rebuild procedures and disclose what host loss
can destroy when the operator has not protected irreplaceable state.

## Bundled Compose dependency qualification

The supported package must declare the exact PostgreSQL and storage implementations,
versions, volume layout, credential bootstrap, filesystem integrity and immutable
write semantics, retention, resource limits and upgrade compatibility. Qualify
local analytical reconstruction and authoritative-state recovery before promising
a complete production installation; no bundled S3 service is required.

Test a fresh installation, restart, application upgrade and dependency upgrade as
distinct operations. PostgreSQL major-version upgrades require a documented
native procedure, compatible catalog/extensions, backup, validation and recovery
plan. Never treat replacing the database image tag as a major-version upgrade.
Apply equivalent rules to storage format changes and encryption/key rotation.

Interrupt dependency upgrades at meaningful boundaries. Show how the operator
identifies the state and safely resumes or restores it. Ordinary application
rollback must not silently downgrade a database or storage format. On empty
replacement infrastructure, restore protected control state and retained inputs,
then rebuild the analytical catalog/files. Test optional analytical backup restore
separately with a consistent catalog/file set and required keys.

These are separate delivery obligations from reducing deployment glue. Preserve
simple installation through standard dependency tooling and clear commands;
avoid a new LeapView database backup or upgrade engine.

## Release behavior by profile

| Behavior | Compose | Operated Kamal |
|---|---|---|
| Update | Documented Compose reconciliation | Candidate readiness, proxy switch, drain and stop prior container |
| Interruption | Measured maintenance/restart bounds | Measured cutover/reconnection and restart-rollback bounds |
| Previous release | Retained immutable artifact and restart procedure | Retained images and matching configuration; no running standby requirement |
| Post-deploy failure | Monitoring and operator procedure where configured | Off-host monitoring, alert, campaign halt and operator rollback/recovery |
| Durable state | Compatible current data; explicit restore for incompatible transitions | Same contract; application rollback does not rewind PostgreSQL or objects |

### Release and artifact policy

Pin Kamal/proxy versions. Record readiness checks, deployment/drain/stop timeouts,
SSE buffering and response timeouts, upload limits, resource limits and supported
release pairs. Deploy CI-built immutable artifacts; verify the image identity
actually running. Serialize releases per environment and promote customers in
explicit batches using ordinary CI/inventory controls.

Retain images, versioned configuration and compatible secret inputs for the
supported rollback window. Align local pruning and registry retention. Test
rollback without registry access; define the fallback and recovery bound if the
prior artifact is absent. A retained container alone does not prove that its old
configuration or credentials are still usable.

Kamal's initial health gate is not continuous application supervision. Configure
off-host monitoring and alerts. Required unavailable, empty, stale or failed
checks stop further customer promotion. An operator investigates within the
approved response window and rolls back or invokes recovery as appropriate.
Low traffic needs explicit representative checks; empty metrics cannot mean
success. Document the monitoring-outage procedure and release freeze. No automatic
metrics-driven rollback, retained warm release or custom controller is assumed.

Measure the time from failure detection through operator response, restart,
readiness and successful user requests. Include the support coverage actually
sold. Ordinary application rollback cannot downgrade incompatible durable state.

### Background work and overlap

Define job admission, ownership, draining and fencing during candidate startup,
cutover, rollback and host replacement. A candidate may serve readiness before
it is authorized to claim mutating work. HTTP routing does not establish worker
ownership. Use existing durable claims, idempotency and fencing; prove a stale
worker cannot commit effects after authority transfers.

Kamal's normal update briefly overlaps processes even without a warm standby.
Budget concurrent DuckDB memory, container memory, query admission and temporary
disk. Qualify file access and catalog compatibility during overlap; do not infer
safe concurrent writes to a shared local database file. Bound interrupted queries,
SSE reconnection and upload retries, and preserve queued-work compatibility.

## Availability and assurance gate

Before sale or onboarding, security/privacy and service owners approve customer
uses, data classes, applicable obligations, maintenance windows, support coverage,
maximum interruption, RTO and RPO against operating evidence. The profile provides
single-host application availability. A reboot or failed VPS causes an outage;
managed data-service HA cannot remove it.

Measure detection, operator response, replacement capacity, provisioning, secrets,
certificates, routing/DNS changes, source extraction, transformation, validation
and publication or restoration as applicable. Exercise provider
capacity or region unavailability for any promised disaster scenario. A plan to
buy replacement compute is not proof that it will be available within the RTO.

Preserve the applicable assurance baseline. Where Regulation (EU) 2024/2690
applies, assess Annex section 4 backup, redundancy and testing requirements;
record sufficient resources and evidence rather than assuming backups alone
satisfy redundancy. Assess DORA supplier obligations for the financial customer's
function. Customer acceptance cannot waive legal duties. Requirements exceeding
this profile need a separately qualified topology before onboarding.

Maintain isolated recovery copies and scheduled restoration tests, including the
CIS IG2 safeguard 11.5 minimum quarterly sampling requirement where applicable.
Certification and legal compliance claims require their own scope and evidence.

## Targeted failure evidence

- **Upgrade/rollback:** failed candidate readiness, failure after cutover, rollback
  after new writes, unavailable registry, stale configuration and expired secrets.
- **Migrations:** interruption/retry preserves one durable operation and correct
  schema/catalog state; serving identities cannot assume migration privileges.
- **Management outage:** interrupt the runner before/after cutover; inspect actual
  host state and safely resume/recover without duplicate effects. Continued serving
  must not require the central deployment runner.
- **Secrets outage:** restart without the secret service, then with expired/revoked
  credentials. Document permitted cached/projected credential behavior; fail closed
  without an approved path and include startup dependencies in recovery time.
- **Overload:** saturate memory, query concurrency and temporary disk during overlap;
  prove bounded failure, truthful readiness and capacity for operator recovery.
- **Host failure:** replace the VPS, preserve authoritative state, rebuild local
  analytics into fresh metadata/files, fence its predecessor and verify access/TLS.
- **Analytical corruption:** fresh-pool rebuild, reset checkpoints, interrupted
  extraction, unavailable sources, failed validation, lost-ack activation, repeated
  snapshot numbers, reader-safe retirement and bounded disk use.
- **Authoritative-state loss:** restore protected records and inputs, validate
  governed reads/writes and replay protection, and record achieved RTO/RPO.
- **Optional analytical restore:** match catalog/files and prove consistency if
  this recovery path is offered. Otherwise measure complete source rebuild time.
- **Maintenance:** qualify OS/Docker/proxy updates and reboot, managed database
  failover/major upgrades, and bundled Compose dependency interruption separately.

## References

- [DuckLake catalog creation](https://ducklake.select/docs/stable/duckdb/usage/connecting),
  [storage](https://ducklake.select/docs/stable/duckdb/usage/choosing_storage) and
  [recovery](https://ducklake.select/docs/stable/duckdb/guides/backups_and_recovery).
- [Kamal deployment](https://kamal-deploy.org/docs/commands/deploy/),
  [rollback](https://kamal-deploy.org/docs/commands/rollback/),
  [proxy](https://kamal-deploy.org/docs/configuration/proxy/) and
  [accessories](https://kamal-deploy.org/docs/configuration/accessories/).
- [NIS2 implementing requirements](https://eur-lex.europa.eu/legal-content/EN/TXT/PDF/?uri=CELEX%3A32024R2690),
  [DORA](https://eur-lex.europa.eu/eli/reg/2022/2554/oj) and
  [CIS data recovery](https://cas.docs.cisecurity.org/en/latest/source/Controls11/).

These are proposed requirements, not claims that the current product or selected
provider configuration passes qualification.
