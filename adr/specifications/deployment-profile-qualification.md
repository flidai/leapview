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
| Operated VPS, PostgreSQL and local analytical rebuild | Managed launch | Selected stack configuration, region/plan qualification, rebuild time, protected customer state, isolation and eligibility |
| Bundled Compose dependencies | Advertising bundled single-host production support | Local persistence, rebuild/publication and PostgreSQL dependency lifecycle evidence |

Record the release, configuration, providers, resource layout, reviewer and dated
results for each gate. The managed profile uses one application VPS per customer
with local SSD and a separate customer PostgreSQL VPS operated by LeapView.
PostgreSQL has one primary and no standby in the selected v1 profile. Compose uses
local persistent storage and bundled PostgreSQL by default. Neither profile requires S3 for
analytical serving. Managed operations additionally require monitoring and off-host
protection of irreplaceable state; self-hosters configure those integrations.
Analytical output backups are optional when full reconstruction is qualified.

## Operated profile qualification

### Ownership and locations

Record European application, database, object and backup regions, plus GitHub,
telemetry, access, email, optional edge and support processing locations. Name the accountable party,
implementation, maintenance process, escalation and recovery evidence for each:

| Responsibility | Required coverage |
|---|---|
| Hetzner resources | Account ownership, dedicated customer VPS, quotas, replacement capacity and region failure |
| Ubuntu LTS and Docker | Qualified OS baseline, vulnerability updates, Docker upgrades, reboot windows, firewall and recovery |
| Kamal and proxy | Pinned versions, application updates, rollback, HTTPS renewal, trusted client identity and SSE |
| Self-operated PostgreSQL | Customer database VPS, roles, TLS, version/OS maintenance, PITR, archive monitoring, retention and restoration to a replacement host |
| Local SSD | Persistent mount layout, isolated paths, integrity, capacity, rebuild/publication and safe cleanup |
| Hetzner Object Storage | Protection of customer state/retained inputs, pgBackRest compatibility, access isolation, retention, deletion protection, restore/export and keys |
| Better Stack | Monitoring coverage, redacted logs, alerts, on-call, status pages, retention and processing locations |
| Tailscale | Customer-scoped operator/runner access, OIDC trust, operator MFA, revocation and recovery access |
| GitHub Actions, GHCR and Trivy | Environment protections, scoped secret delivery, immutable release images, vulnerability gates and remediation |
| PostgreSQL credential storage | UI/API/bootstrap authorization, ciphertext isolation, key/version lifecycle and recovery |
| Cloudflare, when enabled | SSE, uploads, caching exclusions, origin TLS/access, client identity and data processing |
| Postmark | SMTP/TLS, sender domains, retry/bounce handling, scoped access and recipient/content processing |
| Operations | Retained audit records, support coverage, incident response and supplier exit |

OpenTofu provisions declared infrastructure; cloud-init bootstraps access; Ansible
configures and maintains hosts, PostgreSQL and pgBackRest. Kamal owns application
and proxy deployment. Do not let Compose and Kamal manage the same containers or
let application releases implicitly upgrade PostgreSQL. LeapView owns database
maintenance, backup operation, incident response and coordinated recovery. External
database vendors are not required by this profile.

### Selected supporting services and credential evidence

The ADR selects these technologies; qualification approves a concrete configuration
for production. Do not require self-hosters to subscribe to them.

- **Better Stack:** inject application, database, archival and disk failures and
  verify actionable alerts and escalation. Check log/trace redaction, metrics
  labels, retention, processing region and access isolation. Exclude credentials,
  query results and sensitive request bodies; do not record credential UI sessions.
- **Tailscale:** scope GitHub OIDC trust and ephemeral runner grants to the intended
  customer/environment. Test operator revocation and denied cross-customer access.
  Verify database/SSH authorization separately, record audit coverage, and rehearse
  recovery access if normal identity or control services are unavailable.
- **GitHub/Trivy:** verify plan support for private-repository environment protections,
  permitted deployment refs, reviewed/pinned workflows, untrusted-code separation,
  least-privilege jobs and per-customer secrets. Scan the actual release digest and
  enforce documented remediation/exception rules. Keep infrastructure, runtime,
  migration and backup privileges separate. Changing a GitHub secret must require
  explicit deployment/reload; protect Kamal's target-host secret files.
- **Customer credentials:** use the same authorized/audited service for UI, API and
  bootstrap. Test secret redaction, version validation/activation, pool refresh,
  in-flight behavior and cross-scope ciphertext rejection. Reject incorrect keys
  without replacing them. Exercise resumable key rotation, retained-backup
  decryption and restoration using a separately protected per-deployment keyring.
  Credential formats and lifecycle details require the focused ADR identified by
  ADR-0025 before implementation.
- **Cloudflare:** test both direct ingress and the optional proxy path. Verify SSE
  delivery/reconnection, upload bounds, cache exclusions, origin TLS/certificate
  renewal, origin restrictions and trusted client identity. Record plaintext access
  and any required regional processing controls; default edge plans do not establish
  EU-only processing.
- **Postmark:** verify authenticated TLS delivery, sender setup, bounded retries,
  bounce visibility and secret redaction. Demonstrate configurable SMTP so email
  remains provider-replaceable; qualify message processing and retention separately.

### Portability and data access evidence

Public installation must not require a managed database provider, LeapView account
or access grant to LeapView staff. Keep provider provisioning separate from
portable PostgreSQL/application configuration. Future customer-owned infrastructure
requires its own qualification and explicit, revocable delegation of operator
access; the v1 Hetzner qualification is not a universal hosting guarantee.

Record the infrastructure, backup, telemetry, secret, email and optional edge
suppliers used, their locations and actual data/credential access. Assess their
processor/subprocessor roles and contracts as applicable. Removing a database
operator does not remove Hetzner or other suppliers from this assessment. Test
operator access scope/revocation, audit retention, telemetry exclusion and backup
key separation. Distinguish normal database administration from infrastructure,
console and exceptional support access. Do not infer exclusive plaintext access,
provider-inaccessible live memory, or compliance from encryption or self-operation.

### Isolation and durable state

Use separate customer application and database VPSs with scoped deployment
credentials. Specify PostgreSQL roles and isolated filesystem paths,
backup bucket resources/credentials, backup access and shared administration.
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

Qualify self-operated PostgreSQL versions, privileges, TLS, minor/major upgrades,
OS maintenance, pgBackRest PITR and independent restoration. A standby/failover
mechanism is not required by the selected v1 profile. Verify the backup policy
through restoration with the original database host unavailable. Application-state
restoration is distinct from analytical reconstruction and must preserve authorization and replay safety.

For managed customers, require off-host recoverable copies of customer state,
retained uploads, necessary authored artifacts and protected configuration/keys.
Use the selected Hetzner Object Storage destination for managed backups; qualify
pgBackRest and retained-file protection against its actual API and retention behavior.
Document shared provider/account risks and independently controlled copies required
by the recovery commitment. Self-hosters can choose supported alternatives.
Verify access separation, retention, deletion behavior and restore testing. A
backup on the same VPS is insufficient for host-loss recovery. State the accepted
RPO, including upload acknowledgement versus durable-copy completion.

Backups of fully rebuildable analytical outputs are optional. If used, qualify a
consistent catalog/file recovery point including cleanup and retention behavior.
PostgreSQL PITR alone does not recover local Parquet files. If not used,
prove source reconstruction fits recovery commitments and document upstream-outage
behavior. Do not restore older control state merely because analytical SSD data
was lost. Use pgBackRest for PostgreSQL recovery and established tools for
retained files; backups of PostgreSQL do not include external artifact directories.

Self-hosted startup requires no off-host storage, monitoring service or backup
account. Ship public backup/export/rebuild procedures and disclose what host loss
can destroy when the operator has not protected irreplaceable state.

### PostgreSQL lifecycle and recovery qualification

Use reviewed, version-pinned Ansible roles and native PostgreSQL/pgBackRest tools.
Prove fresh provisioning and safe reapplication, scoped role bootstrap, verified
TLS and host firewall rules. Exercise runtime, migrator and maintenance identities
separately. Use direct or qualified session-compatible connections for session
advisory locks; do not place every client behind transaction-mode PgBouncer.

Record the supported major version and package origin, minor/security patch
procedure, any package holds and their update/overdue-alert path. Qualify scheduled,
staggered reboots and service restart behaviour. Cloud-init runs bootstrap only;
ongoing changes must reconcile existing hosts. Budget connection pools, memory per
query operation, autovacuum, WAL and backup work from actual VPS resources.

Rehearse a PostgreSQL major upgrade against restored data, including preflight,
extensions/catalog compatibility, writer quiescence, validation and a fresh backup.
Record the last safe rollback point. Starting a `pg_upgrade --link` target makes
its linked old cluster unsafe to restart. Use independent recovery copies or
qualified copy/clone procedures. Reverting after writes were accepted on the new
cluster requires an explicit reconciliation/data-loss decision. Measure the full
service interruption; neither a package command nor `pg_upgrade` guarantees a
fixed restart or sub-minute maintenance window.

Configure off-host base backups plus continuous WAL archiving with pgBackRest,
retention and standard scheduled execution. Record backup encryption and key
recovery independent of the database VPS, storage credential scope and deletion
protection. Test retention/expiration with the destination's protection settings.
Monitor base-backup age, archival failures/backlog, WAL growth and disk capacity.
An archive interval is not a hard RPO guarantee during upload failure; define
whether writes may continue when protection falls behind and alert accordingly.

Measure PITR and complete primary-host-loss restoration, including detection,
on-call coverage, response, replacement capacity, provisioning, keys, transfer,
WAL replay, endpoint changes and application validation. Use a disposable isolated
restore destination on a schedule and after material tooling changes. Verify
restored roles/configuration, replay safety, external effects and catalog/files;
rebuild analytical pools when their restored references cannot safely be served.
Neither successful backup exit status nor retained files alone qualifies recovery.

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
single-host availability in both tiers. An application or database reboot/host
failure can cause an outage; a separate database VPS provides resource isolation,
not redundancy. Accepted downtime does not set an acceptable data-loss bound.

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
- **Credential recovery:** restart with GitHub unavailable using provisioned host
  secrets. Restore a fresh host using independent encrypted bootstrap/key recovery.
  Test missing/invalid keys, retained backups after rotation and revoked credentials.
  Any optional external resolver separately proves outage and expiry behavior.
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
- **Database host loss:** restore pgBackRest backups and WAL into an empty
  replacement, without access to the original primary; fence any returning primary
  and validate permissions, jobs, external effects and DuckLake file references.
- **Backup failure:** interrupt WAL archiving, exhaust repository capacity, deny
  credentials and lose the primary encryption-key copy. Verify alerts, permitted
  write behaviour, key recovery and measured restore/data-loss bounds.
- **Maintenance:** qualify OS/Docker/proxy updates and staggered reboot, PostgreSQL
  minor and major upgrades, and bundled Compose dependency interruption separately.

## References

- [PostgreSQL upgrades](https://www.postgresql.org/docs/17/pgupgrade.html),
  [version policy](https://www.postgresql.org/support/versioning/),
  [pgBackRest](https://pgbackrest.org/user-guide.html), and
  [PgBouncer compatibility](https://www.pgbouncer.org/features.html).
- [Hetzner data protection](https://docs.hetzner.com/general/company-and-policy/data-protection-at-hetzner/)
  and [EDPB processor roles](https://www.edpb.europa.eu/sme/learn-the-basics/data-controller-or-data-processor_en).

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
