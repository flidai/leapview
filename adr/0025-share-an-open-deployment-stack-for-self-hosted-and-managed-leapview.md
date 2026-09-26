# ADR-0025: Share an open deployment stack for self-hosted and managed LeapView

Status: proposed

Decision date: pending review

Proposal date: 2026-09-25

Last revised: 2026-09-26

Implementation: pending; this proposal does not establish production readiness

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0020](0020-adopt-a-postgresql-centered-target-data-architecture.md),
proposed single-host Compose exception to managed PostgreSQL, local analytical
file storage, and rebuild-based recovery for explicitly reconstructible analytical
outputs. PostgreSQL control authority, privilege separation and safe publication
remain unchanged. This proposal does not amend the accepted record until reviewed.

Related: [ADR-0003](0003-retain-narrow-infisical-resolver.md),
[ADR-0015](0015-adopt-durable-audit-and-compliance-controls.md),
[ADR-0020](0020-adopt-a-postgresql-centered-target-data-architecture.md),
[ADR-0021](0021-adopt-a-local-first-analytics-development-workflow.md),
[deployment-stack reuse research](specifications/deployment-stack-reuse-research.md),
[deployment profile qualification](specifications/deployment-profile-qualification.md),
[production Compose package](../deploy/compose/README.md),
[Hetzner deployment](../deploy/hetzner/README.md)

## Context and problem statement

LeapView is an open-source, self-hostable product. We also intend to operate
dedicated customer environments on Hetzner for enterprise managed hosting.
Making that service reliable requires repeatable provisioning, controlled
upgrades, monitoring, recovery, and accountable operator access.

Self-hosters need a low-friction installation. Our operators can use a richer
standard deployment stack when it reduces custom infrastructure work. Managed
means that we operate the deployment; it does not require a new LeapView cloud
product. V1 targets European Hetzner infrastructure. Operation on customer-owned
infrastructure is a future deployment option, not a v1 onboarding requirement.

These requirements should improve the deployment capabilities available to
self-hosters. Maintaining separate public and proprietary runtime or deployment
implementations would duplicate correctness work and allow the two offerings to
diverge. Conversely, placing every fleet-management tool in the default Compose
installation would make ordinary self-hosting unnecessarily difficult.

The repository already contains Compose, Caddy, Terraform-based Hetzner
provisioning, PostgreSQL tooling, and Prometheus rules. This proposal establishes
their long-term relationship and introduces additional supporting technologies.
Existing implementation choices inform migration work; they do not establish
that the target architecture is already qualified.

## Decision drivers

- Keep a complete, independently operable open-source product.
- Make managed-hosting reliability improvements reusable by self-hosters.
- Minimize custom infrastructure code and recurring operational work by reusing
  established deployment tools and qualified managed data services.
- Make basic self-hosting possible with Docker Compose and minimal configuration.
- Operate dedicated customer environments with established tools, while keeping
  the product independent of Kubernetes.
- Preserve deployment portability without building a v1 customer-cloud platform.
- Give infrastructure, host configuration, application deployment, and data
  migrations distinct owners.
- Preserve data through failed upgrades and make recovery demonstrable.
- Keep customer isolation, access control, and operational evidence explicit.
- Qualify supporting services against LeapView's behavior and service objectives.

## Considered options

1. **Public application with a proprietary deployment stack.** This permits
   independent commercial tooling, but duplicates lifecycle behavior and leaves
   self-hosters without the improvements developed for managed production.
2. **One comprehensive Compose stack containing all operational services.** This
   makes a large installation reproducible, but couples ordinary application
   operation to fleet, secrets, and observability infrastructure.
3. **A shared open deployment stack with optional operational components.** The
   same application artifacts and lifecycle contracts serve both offerings;
   operators choose an appropriate topology and supporting services.
4. **One Kubernetes-only installation experience or a custom fleet platform.**
   This standardizes orchestration, but adds avoidable prerequisites to ordinary
   self-hosting or requires building infrastructure management software ourselves.

## Decision outcome

Choose option 3. Publish the reusable deployment stack alongside LeapView and use
it to operate managed customer environments. Managed hosting is an operational
responsibility built on this stack. A private repository and custom management
service are not prerequisites.

### V1 scope and future portability

V1 has two public deployment packages for the same product:

- **Self-hosted:** Docker Compose with a straightforward first-run experience,
  persistent dependencies, and optional HTTPS and monitoring.
- **Operated by LeapView:** one dedicated application VPS per customer, initially
  on European Hetzner infrastructure, using Docker and Kamal with restart-based
  rollback. Managed PostgreSQL hosts authoritative application state and the
  DuckLake catalog; local SSD stores analytical files. S3 and Kubernetes are not
  required for analytical serving. Managed operations additionally protect
  irreplaceable customer state through off-host backups or durable copies.

LeapView prepares source data for BI. For sources supporting a complete rebuild,
DuckLake serving datasets are replaceable pipeline outputs. Local analytical data
and its catalog can be rebuilt and republished after loss or corruption. Customer
uploads without another retained copy, authored state and required artifacts are
not automatically reconstructible. Classify them and preserve them separately.

Restart-based rollback and measured downtime are accepted. A continuously running
previous release and automatic metrics-driven rollback are not v1 requirements.
Kubernetes/Argo and other deployment platforms remain researched alternatives;
they are not additional v1 packages we commit to maintain.

Architectural acceptance does not clear either profile for production. The
companion specification defines separate gates for the operated VPS/data-service
profile and bundled Compose dependencies. We own OS, Docker and proxy maintenance;
managed PostgreSQL reduces database operations within qualified provider
responsibilities.

V1 includes repeatable installation, upgrades, rollback, monitoring, access, and
tested recovery using existing tooling. A customer portal, automatic signup,
billing platform, custom fleet controller, general cloud abstraction, and
automated customer-account onboarding are outside v1 scope.

Keep application images, Compose/Kamal configuration and lifecycle interfaces
public and portable. Isolate Hetzner-specific provisioning from application
packaging. Later, the same product can be operated on our infrastructure or the
customer's infrastructure, with qualification of that environment. V1 does not
promise support for untested hosts, data providers or customer-account onboarding.

### Public software and private operational information

The public repository owns:

- The application, released container images, and versioned configuration and
  administrative interfaces.
- Compose packages, Caddy configuration, deployment adapters, infrastructure modules,
  host automation, Kamal configuration, and sanitized deployment examples.
- Migration and compatibility checks, readiness and graceful shutdown behavior,
  upgrade and rollback tooling, and backup/export/restore capabilities.
- Reusable metrics, dashboards, alert rules, operational runbooks, and
  qualification harnesses.

Generic fixes and improvements developed while operating managed customers must
return to these public components. Managed hosting uses the same published
application release artifacts; it must not depend on a private runtime fork or
private implementation of a required recovery or migration operation.

Actual customer inventories, environment declarations, access assignments,
incident records, and customer-specific procedures remain private. A private
operations repository may version suitable configuration and internal procedures
when needed. Secrets belong in a secret manager; infrastructure state belongs in
a protected, backed-up remote backend with locking. Neither belongs in Git.
Billing systems and a future customer portal may be private without changing
the public deployment contract.

Self-hosting requires no LeapView-managed account, telemetry endpoint, or fleet
service. Public examples must explain required external dependencies and how an
operator supplies them independently. This ADR does not change the product's
license or third-party component licenses.

### Reference technologies and their responsibilities

The following are proposed stack choices, subject to implementation and the
confirmation evidence below. They are not a statement that every integration
already exists.

| Layer | Proposed reference | Responsibility and inclusion |
|---|---|---|
| Application runtime | Docker Engine and Docker Compose | Default self-hosted application lifecycle. Keep the base package small. |
| HTTPS edge | Caddy for Compose; kamal-proxy for managed installations | One edge per profile with qualified TLS, SSE and draining behavior. |
| Operated release deployment | Kamal and Docker Engine | Public configuration for one dedicated customer application VPS; health-gated updates and restart-based rollback. |
| Infrastructure provisioning | OpenTofu with the official Hetzner provider | Public modules for VPSs, networks, firewalls, and load balancers. Qualify existing Terraform modules and state before changing their runner. |
| Host configuration | Ansible | Public OS baseline, Docker and agent configuration. LeapView owns host patching, reboots and replacement procedures. |
| Observability | Prometheus-compatible metrics, Grafana Alloy, and Grafana | Public collection configuration, dashboards, and alerts. Optional local monitoring package; managed operation uses a central backend. |
| Deployment secrets | Infisical | Reference managed secrets integration with scoped machine identities. Self-hosters can supply supported secret inputs without subscribing to Infisical Cloud. |
| Release production | GitHub Actions and GHCR | Produce immutable release images and provenance; deployments verify and promote existing artifacts. |
| Data services | Managed PostgreSQL plus local SSD for operated deployments | PostgreSQL holds authoritative control state and DuckLake metadata; SSD holds analytical files. Compose can bundle PostgreSQL. Back up irreplaceable state and qualify analytical rebuilds. |
| Recovery storage | Operator-selected off-host destination | Optional self-hosted configuration; required protection for managed customer state that cannot be recreated. Managed S3 is a candidate backup/upload destination, not a required analytical read path. |
| Initial managed infrastructure | One dedicated Hetzner application VPS per customer | Public provider adapter; size dedicated-vCPU capacity and transient release overlap from measured workloads. Single-host availability limits apply. |

Compose and Kamal are alternative owners of application container lifecycle.
They must not reconcile the same containers. Both consume shared LeapView
interfaces for initialization, compatibility, migrations, readiness and recovery.
Publish Kamal configuration and thin integration with these interfaces. Do not
build a general release controller or warm-slot manager around Kamal.

Qualify the Kamal path against the application and recovery contracts before
production adoption. A failure to meet them requires revisiting the configuration
or proposal. Kamal is deployment tooling; it does not replace host maintenance,
continuous monitoring, incident response or data recovery.

For Compose, Caddy forwards directly to LeapView. Managed installations use
kamal-proxy for HTTPS and release routing. Qualify SSE buffering and timeouts,
uploads, client identity, draining and certificate renewal. Private administrative
and metrics endpoints must remain protected.

OpenTofu owns declared provider resources; Ansible owns the OS baseline, Docker
and supporting host configuration; Kamal owns application deployment and its proxy
lifecycle. Define maintenance ownership for each component without competing
reconcilers. Managed data-service maintenance follows separately qualified
provider procedures.

Grafana is not an application dependency. A local metrics backend and Grafana may
be supplied as an optional monitoring package; managed hosting may use Grafana
Cloud or another compatible central backend. Telemetry export is operator
configured and must exclude credentials and customer data by default.

### Deployment profiles and service boundaries

Publish a supported single-host Compose profile that provisions its required local
dependencies with pinned images and persistent storage. It must not require users
to purchase an external database, subscribe to a secret manager, or learn our
operator orchestration stack before first use. Generate initial credentials and
bootstrap the required database roles through supported tooling. Document the
small set of inputs, data locations, updates and backup responsibilities.

This proposes a single-host Compose exception to ADR-0020's managed PostgreSQL
baseline and a production local-filesystem analytical profile. PostgreSQL remains
authoritative for control state; runtime, migration and maintenance privileges
remain separate. The DuckLake catalog describes replaceable analytical outputs
and must be rebuilt together with them when they are lost. Existing filesystem
adapters and development fixtures are useful foundations, not production evidence.
ADR-0021 continues to govern local analytics development.

Qualify persistent volume layout, filesystem integrity, bounded retention, capacity,
PostgreSQL major upgrades, rebuild/publication and recovery after interrupted
upgrades before advertising production support. Self-hosters may use external
PostgreSQL; basic Compose installation bundles it with persistent local storage.
No external storage account, Grafana or configured backup destination is required
to start and serve dashboards.

| Capability | Self-hosted Compose | Operated Kamal |
|---|---|---|
| Analytical serving | Local persistent filesystem | Local SSD on customer VPS |
| PostgreSQL | Bundled by default; external supported | Qualified managed service |
| Monitoring | Optional integration | Monitoring and alerts required; Grafana is a replaceable tool choice |
| Irreplaceable-state backups | Operator configures destination, schedule and recovery policy | Off-host protection and restore testing required |
| Rebuildable analytical backups | Optional | Optional when qualified source rebuild meets commitments |
| S3/Kubernetes | No installation prerequisite | No analytical-serving prerequisite |

Publish backup/export and rebuild procedures in the public tooling. Optional backup
configuration does not promise recovery of the only local copy after host loss.
Caddy remains the optional Compose HTTPS edge; Kamal supplies the managed edge.
Both profiles declare single-host application availability limits.

Managed environments dedicate application compute, data-service resources, and
credentials to the customer. Sharing a management or observability service does
not permit cross-customer data access. Shared infrastructure administration must
use scoped identities and preserve customer isolation.

Managed v1 assigns each customer a separate application VPS, scoped deployment
credentials, PostgreSQL resources/roles and isolated local data directories.
Record database, optional bucket, provider administration and backup isolation
boundaries and reject cross-customer access. Managed service resource isolation
does not imply a physically dedicated database or storage server.

An application VPS failure or maintenance reboot interrupts that customer's
service until restart or replacement. Lost local data additionally requires
analytical rebuild or a qualified analytical restore. Managed PostgreSQL
preserves application state but cannot restore missing local analytical files.
Recovery depends on source availability, full extraction and validation; include
those dependencies in measured downtime. Qualify authoritative-state restoration
separately from analytical reconstruction.

A future HA offering requires independent application failure domains, suitable
routing, session/SSE and background-work coordination, and matching data-service
availability. Higher availability is a separately qualified topology and does not
in itself require Kubernetes. A dedicated VPS does not imply exclusive ownership
of its physical host.

Hetzner private networking is not an encryption or host-firewall substitute.
Use TLS for database and cross-host application connections and enforce host
firewalls. Where a Hetzner load balancer fronts the TLS edge, use TCP passthrough
to retain encryption to the application host. Qualify certificate issuance and
renewal across nodes and trusted client-IP forwarding.

### Application releases, rollback, and recovery

Define one public release contract containing the image digest, configuration
compatibility, schema/catalog compatibility, supported upgrade paths, and
rollback eligibility. Promote the same artifact through internal qualification,
selected managed customers, and broader rollout within maintenance windows.
Serialize mutations per environment and stop promotion on failed checks.

The managed profile uses Kamal's standard health-gated deployment: start the
candidate, check readiness, switch traffic, drain and stop the prior container.
Reserve capacity for temporary process overlap and analytical work. Qualify
candidate failure before cutover, SSE reconnection and bounded shutdown.

Rollback restarts a compatible prior release; it does not depend on a warm
standby. Retain the required immutable images and matching release configuration,
and configure local pruning and registry retention for the supported rollback
window. Test rollback with the registry unavailable and define how a missing
local artifact affects recovery. Secrets must remain valid or be safely refreshed.

Continuous off-host monitoring observes the deployment after cutover. A failed or
unverifiable release halts the customer rollout campaign and alerts the operator;
the operator follows a bounded rollback or recovery procedure. Do not infer
post-deployment health supervision or automatic analysis rollback from Kamal's
initial readiness check. Use ordinary CI gates, customer inventory and runbooks;
no bespoke metrics-driven release controller is required.

Compose declares its own interruption and restart-based rollback bounds. Both
profiles use the same compatibility and recovery contracts. Define background-job
admission, draining, ownership and fencing during candidate overlap and rollback;
HTTP routing does not determine which process may mutate durable state.

Both releases use current durable data. Schema and data-format evolution must
preserve the declared rollback window, using staged compatible changes where
appropriate. Destructive changes cannot occur while an older supported release
still requires the old representation. A rollback-incompatible upgrade must be
identified before execution and require its documented maintenance/recovery path.

Cutover must account for SSE reconnection, in-flight requests, uploads, and
queries. Concurrent versions must preserve job idempotency, leases, and fencing;
starting a candidate must not duplicate scheduled work or publication effects.
Application binary upgrades and authored analytics deployments remain distinct
operations, each preserving the other's durable contracts.

Database backup restoration is disaster recovery, not ordinary application
rollback. Restoring authoritative state and republishing analytical outputs are
separate operations. Application rollback must preserve acknowledged customer
changes; rebuilding analytical data does not recover a historical source snapshot.

### Analytical storage and rebuild contract

Treat data as rebuildable only when retained source inputs, supported full
extraction, transformation definitions and credentials can reproduce the required
serving dataset within the accepted interruption. Incremental history that has
expired upstream and uploads with no independent copy require preservation.
Retain provenance and freshness information so a new extraction is never described
as exact restoration of older data.

Normal refresh uses the existing healthy DuckLake catalog and publishes a new
validated snapshot. For catalog corruption or analytical disk loss, provision a
new physical pool with a fresh DuckLake metadata schema in PostgreSQL and a fresh
SSD directory. Initialize through supported DuckLake operations; do not repair
internal tables manually or reuse an old catalog against empty storage.

Use the existing durable operation/job machinery to fence affected writers,
rebuild the dependency-complete dataset, validate it and activate a new serving
generation. Reset extraction checkpoints for a full rebuild without discarding
unrelated control state. Publication is the control-plane transition after the
candidate is complete; it does not assume an atomic transaction across PostgreSQL
and files. Reconcile interrupted work and publication acknowledgements safely.

Keep healthy old data available during a normal rebuild. Reject affected reads
when the old state is corrupt, and report rebuilding/unavailable until a validated
replacement exists. Reader leases and worker fencing govern retirement. Keep pool,
catalog and snapshot identities distinct in references and cache keys; snapshot
numbers can repeat in a new catalog. Retire old metadata/files only after references
and readers release them, using supported cleanup and maintenance privileges.

Latest successfully validated data is the serving contract. Historical analytical
retention is bounded by active readers, recovery needs and release compatibility;
indefinite history is not required. Capacity must cover current data, candidate
output, query/build spill and cleanup headroom. An optional analytical backup must
pair catalog and files consistently; PostgreSQL PITR alone cannot restore files.

### Recovery storage boundary

The default runtime needs no off-host storage integration. Self-hosters own backup
configuration and the consequences of retaining their only copy on the same host.
The public package supplies supported procedures and clear data classifications.

Managed hosting requires off-host recoverable copies of authoritative state,
customer uploads we retain, necessary authored content/artifacts and protected
key/configuration recovery. Choose an existing backup service, managed S3 or
another qualified destination; a second local directory/volume is not off-host.
Test recovery, retention and deletion behavior against promised RPO/RTO. A managed
PostgreSQL label alone does not prove an adequate backup policy.

Backups of genuinely rebuildable analytical outputs are optional. Use them when
they reduce recovery time or dependence on upstream availability. S3 analytical
serving remains a future separately qualified option for multiple hosts, larger
workloads or different recovery requirements, without becoming a default dependency.

### Service selection and qualification

Hetzner is the selected application VPS provider. Data and operational service
providers remain candidates; qualify the responsibilities below before launch:

- **Application VPS and host operations:** qualify the European Hetzner region,
  dedicated customer assignment, sizing, OS/Docker/proxy maintenance, support and
  replacement capacity. Record patch/reboot behavior, operator access, costs and
  recovery evidence. LeapView owns these duties; Kamal does not outsource them.
- **PostgreSQL:** use an external managed service; evaluate Ubicloud on Hetzner
  for European region, version/privilege compatibility, TLS, replication mode,
  failover, major-version maintenance, support, PITR retention and independently
  usable backup export. Preserve ADR-0020's managed HA production baseline.
- **Local analytical storage:** qualify native filesystem paths and permissions,
  integrity, persistent Docker mounts, disk-full behavior, capacity and performance
  under refresh and release overlap. Test complete disk loss and catalog replacement.
- **Off-host recovery storage:** evaluate managed S3 or established backup services
  for European regions, tenant isolation, protected copies, retention, key recovery,
  restoration/export and cost. Match capabilities to the actual backup or upload
  contract. Immutable runtime objects, if stored there, still need their declared
  conditional-write semantics; backups need their selected tool's storage guarantees.
  An S3-compatible label or versioning alone does not establish backup protection.
- **Operational services:** evaluate Grafana Cloud and Teleport against monitoring
  and privileged-access requirements. Their hosted products are not prerequisites
  for the public deployment stack.

The release and provider qualification results belong in a mutable companion
specification or linked delivery work. This proposal establishes required
behavior, not evidence that vendor defaults meet it. Managed-service assurance
and customer qualification require separate decisions; this deployment proposal
does not establish their legal coverage or customer commitments.

### Availability and assurance boundary

Declare this offering as a recoverable single-application-host service. Establish
customer eligibility, maintenance windows, support coverage, maximum acceptable
interruption, recovery time (RTO) and data loss (RPO) from the service's risk
assessment and measured exercises before contractual commitments. Include outage
detection, operator response, provider capacity, credentials and routing changes
in recovery measurements. Do not invent numeric guarantees from tool defaults.

The simpler stack preserves the applicable security and assurance baseline.
Kamal and European hosting do not establish GDPR, NIS2, DORA, CIS or ISO conformance.
Security/privacy owners must assess the actual service and customer use, including
redundancy requirements. Where applicable, Regulation (EU) 2024/2690 Annex section 4
requires backup, redundancy and recovery testing; accepting downtime does not
waive these duties. Financial-customer commitments require the applicable DORA
supplier qualification. Customers whose requirements exceed this profile need a
separately qualified topology before onboarding.

### Managed operations without a prerequisite platform build

Managed production requires inventory, repeatable provisioning, controlled
releases, monitoring and incident response, tested recovery, scoped access,
credential rotation, and an audit trail. Initially these may be delivered through
versioned configuration, CI workflows, the public tooling, existing operational
services, and documented operator procedures.

A custom fleet API, dashboard, billing integration or Temporal workflow service
is outside v1 scope. Kamal is the proposed operator deployment toolchain. Add further orchestration only when
measured operational needs justify its lifecycle and failure modes. Keep routine
customer requests outside any central management service's availability boundary;
document and test behavior during management outages.

Evaluate existing fleet interfaces and reconcilers before building their
equivalents. Preserve the existing PostgreSQL/River recovery occurrence and
evidence contracts; a deployment controller or external job runner does not
become a second authority for application recovery. Use provider-native backup
operations or established database backup tools rather than implementing backup
engines in LeapView.

Do not use Watchtower-style autonomous image replacement for production releases.
Application upgrades follow explicit release policy; OS, database, proxy, and
agent maintenance also need controlled and tested update procedures.

Linear owns delivery status. A managed-hosting launch-readiness project should
link this ADR and the existing assurance work, with implementation work classified
as public tooling or private operations. It must not presume a custom platform
build. Current implementation documentation changes as capabilities ship.

## Consequences

Self-hosters receive the same reliability improvements used by managed customers.
One application and shared lifecycle interfaces reduce correctness drift, and
public infrastructure modules make Hetzner provisioning reproducible. Customer
operations can begin before a bespoke fleet platform exists.

Supporting Compose and one managed deployment path creates an ongoing integration
and qualification cost. We retain OS, Docker, proxy and host recovery work.
Managed data services add provider dependence and service fees. Public operational
interfaces need compatibility discipline, documentation and sanitized fixtures.
Temporary release overlap requires spare capacity. Single-host outages and
restart-based rollback constrain the customers and service levels supported. HA,
backup retention, managed services, and off-provider storage introduce recurring
costs that must be measured against the offering's service commitments.

A bundled self-hosted database/storage profile adds upgrade, persistence and
recovery qualification duties. Local serving removes remote object reads, but
host loss now requires extraction and computation or a qualified analytical restore.
Full-source access, rate limits and rebuild cost become recovery dependencies. Its simpler installation cannot inherit the
managed profile's availability claims. Future customer-owned hosting remains
possible without funding its provisioning and support matrix in v1.

Publishing deployment automation makes it reusable by other operators, including
competing hosts. Our managed offering differentiates through operation, support,
service quality, and accountable commitments. Keeping operational information
private does not itself provide an isolation or security control.

## Confirmation

Before advertising a deployment profile or promoting it to managed production,
retain reproducible evidence appropriate to that profile:

1. **Independent installation:** a clean operator environment installs the public
   Compose release with documented minimal inputs and bundled dependencies, without
   private source code, paid infrastructure-service accounts or a LeapView-managed
   account. Persisted data survives restart and a documented upgrade. Monitoring
   and managed-service outages do not prevent ordinary application serving
   outside documented dependency requirements.
2. **Shared lifecycle:** Compose and Kamal use the same
   application artifacts and lifecycle contracts, with a single container owner
   per installation.
3. **Upgrade and rollback:** under queries, SSE, uploads, and background work,
   upgrade A to B, commit new writes on B, then roll back to A within the supported
   window. Verify preserved writes, correct authorization, reconnect behavior,
   fenced work, and each profile's documented interruption bound. Kamal proves
   restart-based rollback, artifact availability and safe worker ownership.
4. **Recovery:** restore authoritative state and retained inputs into fresh
   infrastructure; rebuild and publish analytical outputs. Separately test any
   analytical backup restore with matching catalog/files. Verify governed queries
   and writes, measured interruption, data loss and source freshness.
5. **Storage and network behavior:** exercise filesystem integrity, permissions,
   concurrent immutable writes and disk-full behavior; qualify any off-host backend
   against its actual contract. Verify data-service privileges, TLS, certificates,
   network restrictions and protected administrative endpoints.
6. **Operations and isolation:** observe actionable alerts, access revocation,
   credential rotation, deployment history, and rejected cross-customer access.
   Interrupted provisioning and deployment can be safely resumed or reconciled.
7. **HA, where offered:** lose an application host and the database primary under
   load. Verify the documented availability and recovery bounds across the actual
   failure domains; same-host fixtures do not establish this claim.
8. **Interrupted upgrades:** interrupt application migrations and bundled
   dependency upgrades, including PostgreSQL major-version transitions. Resume or
   recover without inconsistent state; serving credentials cannot acquire migration
   privileges. Preserve the recorded recovery point and data-loss bounds.
9. **Management interruption:** stop the deployment runner during an update.
   Inspect and safely resume or recover without duplicate migrations or job effects.
   Prove routine serving continues during central management outages.
10. **Missing observation evidence:** inject unavailable, empty or stale monitoring
    evidence. Halt further customer promotions and alert the operator; exercise the
    bounded investigation/rollback procedure. Missing evidence cannot approve a
    release, and no automatic post-deployment Kamal rollback is assumed.
11. **Secrets outage on restart:** restart an application while its configured
    secret service is unavailable. Prove the declared startup/recovery behavior,
    including credential expiry and denial; fail closed when credentials cannot
    be obtained through an approved path. Include any resulting delay in recovery
    measurements rather than claiming unconditional restart availability.
12. **Analytics overload:** saturate memory, query concurrency and temporary-disk
    budgets under release overlap. Verify bounded admission and failure behavior,
    truthful readiness, and sufficient capacity for release/recovery operations.
13. **Application host loss:** provision an empty VPS, reconnect to managed
    PostgreSQL and rebuild analytical outputs into fresh metadata/files. Fence a
    returning host and preserve authoritative customer writes. Exercise unavailable
    sources, interrupted rebuild, invalid outputs, repeated snapshot numbers and
    failed/lost-ack publication. Test authoritative-state restoration separately.
14. **Service qualification:** approve the measured maintenance/recovery bounds,
    support model, customer eligibility and applicable legal/assurance controls.
    Retain evidence of isolated backups and scheduled recovery testing.

No new qualification is claimed by drafting or accepting this ADR.

## Research references

Official documentation reviewed on 2026-09-25 and 2026-09-26; provider capabilities
must be rechecked when qualifying an implementation:

- [DuckLake storage](https://ducklake.select/docs/stable/duckdb/usage/choosing_storage),
  [catalog initialization](https://ducklake.select/docs/stable/duckdb/usage/connecting),
  [file cleanup](https://ducklake.select/docs/stable/duckdb/maintenance/cleanup_of_files)
  and [backup/recovery](https://ducklake.select/docs/stable/duckdb/guides/backups_and_recovery).
- [Kamal deployment](https://kamal-deploy.org/docs/commands/deploy/),
  [rollback](https://kamal-deploy.org/docs/commands/rollback/),
  [proxy](https://kamal-deploy.org/docs/configuration/proxy/) and
  [accessories](https://kamal-deploy.org/docs/configuration/accessories/).
- [NIS2 implementing requirements, Annex section 4](https://eur-lex.europa.eu/legal-content/EN/TXT/PDF/?uri=CELEX%3A32024R2690),
  [DORA Article 30](https://eur-lex.europa.eu/eli/reg/2022/2554/oj),
  [CIS recovery safeguards](https://cas.docs.cisecurity.org/en/latest/source/Controls11/)
  and [ISO/IEC 27001 scope](https://www.iso.org/standard/27001).
- [Caddy automatic HTTPS](https://caddyserver.com/docs/automatic-https).
- [Hetzner provider](https://github.com/hetznercloud/terraform-provider-hcloud) and
  [OpenTofu S3 state and locking](https://opentofu.org/docs/language/settings/backends/s3/).
- [Hetzner private networks](https://docs.hetzner.com/networking/networks/faq/),
  [firewall limitations](https://docs.hetzner.com/cloud/firewalls/faq/), and
  [load-balancer protocols](https://docs.hetzner.com/networking/load-balancers/faq/).
- [Hetzner object-storage limitations](https://docs.hetzner.com/storage/object-storage/supported-actions/),
  [AWS conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html),
  and [Scaleway conditional writes](https://www.scaleway.com/en/docs/object-storage/api-cli/using-conditional-writes/).
- [Ubicloud HA](https://www.ubicloud.com/docs/managed-postgresql/high-availability),
  [networking](https://www.ubicloud.com/docs/managed-postgresql/networking), and
  [backup and restore](https://www.ubicloud.com/docs/managed-postgresql/backup-and-restore).
- [Grafana telemetry collection](https://grafana.com/docs/grafana-cloud/observe-and-act/send-data/)
  and [Infisical machine identities](https://infisical.com/docs/documentation/platform/identities/machine-identities).
