# Deployment profile qualification

Status: proposed requirements; no profile is qualified by this document

Date: 2026-09-25

Related: [ADR-0025](../0025-share-an-open-deployment-stack-for-self-hosted-and-managed-leapview.md),
[supporting technology research](deployment-stack-reuse-research.md)

## Purpose and release gates

Qualify public Compose self-hosting and the operated Kamal profile using existing
tooling, shared LeapView lifecycle commands, and the UBDR occurrence/evidence
contracts. Linear tracks owners and delivery status. No production capability or
numeric service guarantee is established by this specification.

| Gate | Required before | Open decisions and evidence |
|---|---|---|
| Operated VPS and managed data services | Managed launch | Provider/region selection, host operations, isolation, measured recovery, customer eligibility |
| Bundled Compose dependencies | Advertising bundled single-host production support | Storage implementation, persistence and dependency lifecycle evidence |

Record the release, configuration, providers, resource layout, reviewer and dated
results for each gate. The managed profile uses one application VPS per customer
and external managed PostgreSQL and managed S3-compatible storage. The Compose
profile retains bundled dependencies and can accept external services.

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
| Managed S3 storage | Customer isolation, conditional writes, versioning, retention, deletion protection, recovery/export and keys |
| Operations | Scoped credentials, monitoring, retained audit records, support coverage, incident response and supplier exit |

OpenTofu provisions declared infrastructure; Ansible configures hosts; Kamal owns
application/proxy deployment. Do not let Compose and Kamal manage the same
containers. Managed data providers own only the duties explicitly supported by
their service and contract. LeapView retains coordinated recovery correctness.

### Isolation and durable state

Use one customer application VPS and scoped deployment credentials per customer.
Specify dedicated database resources/roles and bucket resources/credentials,
backup access and all shared administration. Do not infer physical host or storage
hardware exclusivity. Reject cross-customer database, object and operator access;
prove resource exhaustion in one application VPS cannot consume another's compute.
Document any shared data-provider capacity or management failure impact.

Keep PostgreSQL control/catalog state and durable objects outside the application
VPS. Inventory local files and classify them as reconstructible caches/scratch or
state requiring explicit preservation. Acknowledged durable writes must survive
application host replacement. Stage uploads with defined acknowledgement and retry
semantics. Independent data services do not make the application highly available.

Rebuild on an empty VPS and reconnect to intact data. Fence the previous host
before permitting writes, including if it returns after replacement. Recover or
reconcile in-flight jobs safely. Restore older data only for an explicit recovery
scenario, not as an automatic consequence of losing application compute.

### Managed data-service lifecycle

Qualify PostgreSQL versions, extensions/catalog behavior, privilege separation,
connection security, failover, major upgrades and independently usable backup
export. Qualify S3 conditional creation together with versioning, retention and
object recovery. Verify isolation of recovery copies and access paths from a
compromised production identity; versioning alone is insufficient protection.

Exercise coordinated PostgreSQL/catalog/object/key restoration after corruption
and provider loss. Test provider or service migration and account/credential loss
within the declared recovery scope. Include object garbage collection, retention,
key availability and secret-service dependencies in the recoverable-point proof.
Use native provider operations or established backup tooling; no custom backup
engine. Measure cross-provider latency, throughput, recovery transfer time and cost.

## Bundled Compose dependency qualification

The supported package must declare the exact PostgreSQL and storage implementations,
versions, volume layout, credential bootstrap, object-write/versioning semantics,
retention, resource limits and upgrade compatibility. Qualify the selected storage
against LeapView's immutable-write and recovery contracts before promising a
complete production installation.

Test a fresh installation, restart, application upgrade and dependency upgrade as
distinct operations. PostgreSQL major-version upgrades require a documented
native procedure, compatible catalog/extensions, backup, validation and recovery
plan. Never treat replacing the database image tag as a major-version upgrade.
Apply equivalent rules to storage format changes and encryption/key rotation.

Interrupt dependency upgrades at meaningful boundaries. Show how the operator
identifies the state and safely resumes or restores it. Ordinary application
rollback must not silently downgrade a database or storage format. Coordinated
restore must cover control state, catalog, objects and required keys, including
retention/garbage-collection interactions, on an empty replacement environment.

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
certificates, routing/DNS changes and restoration as applicable. Exercise provider
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
- **Host failure:** replace an unavailable application VPS, preserve current data,
  safely handle its return and verify operator access, TLS and client reconnection.
- **Data disaster:** restore coordinated state to an empty environment, validate
  governed reads/writes and restart protection, and record achieved RTO/RPO.
- **Maintenance:** qualify OS/Docker/proxy updates and reboot, managed database
  failover/major upgrades, and bundled Compose dependency interruption separately.

## References

- [Kamal deployment](https://kamal-deploy.org/docs/commands/deploy/),
  [rollback](https://kamal-deploy.org/docs/commands/rollback/),
  [proxy](https://kamal-deploy.org/docs/configuration/proxy/) and
  [accessories](https://kamal-deploy.org/docs/configuration/accessories/).
- [NIS2 implementing requirements](https://eur-lex.europa.eu/legal-content/EN/TXT/PDF/?uri=CELEX%3A32024R2690),
  [DORA](https://eur-lex.europa.eu/eli/reg/2022/2554/oj) and
  [CIS data recovery](https://cas.docs.cisecurity.org/en/latest/source/Controls11/).

These are proposed requirements, not claims that the current product or selected
provider configuration passes qualification.
