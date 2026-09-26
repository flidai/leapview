# ADR-0025: Share an open deployment stack for self-hosted and managed LeapView

Status: proposed

Decision date: pending review

Proposal date: 2026-09-25

Last revised: 2026-09-26

Implementation: pending; this proposal does not establish production readiness

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0020](0020-adopt-a-postgresql-centered-target-data-architecture.md),
proposed replacement of the mandatory managed HA PostgreSQL baseline with
qualified self-operated PostgreSQL: bundled Compose and an operated single-primary
VPS profile. Also proposes local analytical file storage and rebuild-based recovery
for explicitly reconstructible analytical outputs. PostgreSQL control authority,
privilege separation and safe publication remain unchanged. Also proposes narrowing
[ADR-0003](0003-retain-narrow-infisical-resolver.md) to deployments that explicitly
choose the Infisical integration: the target default stores customer credentials
encrypted in PostgreSQL and requires no external secret service. Its resolver
security invariants remain applicable when that integration is used. These
proposals do not amend the accepted records until reviewed.

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

This proposal defines the target architecture while LeapView is under active
development. Existing implementation choices do not constrain stack selection or
establish that the target architecture is already qualified.

## Decision drivers

- Keep a complete, independently operable open-source product.
- Make managed-hosting reliability improvements reusable by self-hosters.
- Minimize custom infrastructure code and recurring operational work by reusing
  established deployment, database maintenance and recovery tools.
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
  rollback. A separate dedicated PostgreSQL VPS per customer hosts authoritative
  application state and the DuckLake catalog; LeapView operates its single primary
  using Ansible and pgBackRest. Local SSD on the application VPS stores analytical
  files. S3 and Kubernetes are not required for analytical serving. Managed operations additionally protect
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
profile and bundled Compose dependencies. We own application and database host
maintenance, PostgreSQL updates, backup operation and restoration. A database standby and automatic failover are
not part of the selected v1 profile; measured restore-time downtime is accepted.

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
when needed. Deployment and bootstrap secrets belong in GitHub environment secrets
or operator-supplied protected inputs; customer credentials belong in encrypted
application records. Infrastructure state belongs in a protected, backed-up remote
backend with locking. Secret values and infrastructure state must not enter Git.
Billing systems and a future customer portal may be private without changing
the public deployment contract.

Self-hosting requires no LeapView-managed account, telemetry endpoint, or fleet
service. Public examples must explain required external dependencies and how an
operator supplies them independently. This ADR does not change the product's
license or third-party component licenses.

### Reference technologies and their responsibilities

The following technologies are selected for the target stack in this proposal.
Implementation and production qualification remain delivery work; pending evidence
does not leave the technology selection open. The ADR itself remains proposed
until review. Exact versions, plans and resource sizes are qualified separately.

| Layer | Selected technology | Responsibility and inclusion |
|---|---|---|
| Infrastructure | Hetzner Cloud | One dedicated application VPS and one dedicated PostgreSQL VPS per customer in a European region. Size independently from measured workloads; neither tier has redundancy in v1. |
| Operating system | Ubuntu LTS | Standard managed host baseline with scheduled maintenance and security updates. |
| Infrastructure provisioning | OpenTofu with the official Hetzner provider | Public modules for servers, networks, firewalls and storage resources. Protect remote state and locking; qualify existing state before changing runners. |
| Host and database configuration | Ansible; minimal cloud-init bootstrap | Repeatable Docker, PostgreSQL, backup, networking and agent configuration; controlled patching, staggered reboots and replacement. |
| Application runtime | Docker Engine | Immutable release images and persistent local volumes. |
| Self-hosted deployment | Docker Compose; optional Caddy | Minimal public installation with bundled PostgreSQL and optional HTTPS. |
| Managed application deployment | Kamal and kamal-proxy | Health-gated releases, TLS and traffic routing, draining and restart-based rollback. No retained warm release. |
| Database | Self-operated PostgreSQL | Application state, encrypted customer credentials, jobs and DuckLake metadata. Separate customer database VPS with one primary; bundled locally for Compose. |
| Background jobs | River / PostgreSQL | Durable jobs and recovery work without an additional Redis service. |
| Analytical storage | DuckDB and DuckLake on local SSD | Local analytical serving; rebuild and republish only where complete source replay is supported. |
| PostgreSQL backups | pgBackRest and standard scheduling | Base backups, continuous WAL archiving, retention and PITR. |
| Off-host backup destination | Hetzner Object Storage | Encrypted managed PostgreSQL backups and protection for irreplaceable files. Qualify actual storage/retention guarantees. S3 is outside the analytical serving path. |
| Private administration | Tailscale | Customer-scoped operator and deployment access; prefer GitHub OIDC federation for temporary runner access. |
| Public edge / WAF | Cloudflare, optional per deployment | Supported managed edge where customer data-processing requirements permit it; qualify SSE, uploads, certificates and origin protection. |
| CI and release registry | GitHub Actions and GHCR | Build, test and publish immutable images with provenance; deploy approved existing artifacts. |
| Image security | Trivy | Release image vulnerability scanning with a defined remediation and exception policy. |
| Deployment and bootstrap secrets | GitHub Environments / Secrets | Customer/environment-scoped delivery to Kamal and configuration automation; no runtime GitHub secret lookup. |
| Customer credentials | Application-encrypted PostgreSQL records | Source credentials, provider keys and integration tokens managed through authorized UI/API/bootstrap operations. |
| Encryption keys | Per-deployment, versioned keyring | Provision separately from the database; keep an independent encrypted recovery copy and support rotation. |
| Observability | Better Stack | Managed monitoring, logs, alerts, on-call and status pages. Portable telemetry and public collection configuration; no separately operated Grafana backend required. |
| Transactional email | Postmark for managed hosting; configurable SMTP for self-hosting | Provider-replaceable application email with scoped credentials and delivery monitoring. |

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

OpenTofu owns declared provider resources; cloud-init establishes minimal host
access; Ansible owns repeatable OS, Docker, PostgreSQL and backup configuration.
Kamal owns application deployment and its proxy lifecycle. The database has an
independent maintenance lifecycle; application deployment must not restart or
upgrade it implicitly. Use established roles and tools with pinned versions and
reviewed configuration, rather than a custom database controller.

Better Stack is the selected managed observability service. Keep application
health endpoints and telemetry portable; public collection configuration must
allow self-hosters to choose their own backend. A separate Grafana installation
is optional. Telemetry export is operator configured and must exclude credentials,
sensitive request bodies and customer query/data payloads by default. Do not
enable session replay on credential administration screens.

Tailscale provides private operator and deployment connectivity. Restrict grants
by customer and role, require operator identity controls, and qualify revocation,
temporary runner membership and recovery access. Network membership does not
replace PostgreSQL/application authorization or operation-level audit records.

Cloudflare is optional in front of kamal-proxy and does not own application
release switching. Configure verified origin TLS, trusted client forwarding,
cache exclusions and origin access restrictions. Qualify SSE, upload limits and
certificate issuance/renewal for both direct and proxied ingress. When enabled,
Cloudflare terminates TLS and can process plaintext BI traffic and submitted
credentials. Its default service must not be represented as EU-only processing;
qualify the required regional controls and contract or omit it for that deployment.

### Deployment profiles and service boundaries

Publish a supported single-host Compose profile that provisions its required local
dependencies with pinned images and persistent storage. It must not require users
to purchase an external database, subscribe to a secret manager, or learn our
operator orchestration stack before first use. Generate initial credentials and
bootstrap the required database roles through supported tooling. Document the
small set of inputs, data locations, updates and backup responsibilities.

This proposes replacing ADR-0020's mandatory managed HA PostgreSQL baseline
for both selected profiles and adding a production local-filesystem analytical
profile. The operated service uses one PostgreSQL primary on its own VPS; Compose
bundles PostgreSQL on the application host by default. PostgreSQL remains
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
| Installation and infrastructure | Compose; operator chooses host | Kamal, OpenTofu and Ansible on European Hetzner infrastructure |
| Analytical serving | Local persistent filesystem | Local SSD on customer VPS |
| PostgreSQL | Bundled by default; external supported | Separate customer VPS; self-operated single primary with pgBackRest |
| Customer credentials | Encrypted PostgreSQL records through UI/API/bootstrap | Same implementation and lifecycle |
| Bootstrap secrets | Operator-supplied environment or protected files | GitHub environment secrets delivered to protected host configuration |
| Monitoring | Optional integration; portable health endpoints and telemetry | Better Stack configured and operated by LeapView |
| Private access and edge | Operator's choice; optional Caddy HTTPS | Tailscale; optional Cloudflare in front of kamal-proxy |
| Irreplaceable-state backups | Operator configures destination, schedule and recovery policy | pgBackRest and Hetzner Object Storage; off-host protection and restore testing required |
| Rebuildable analytical backups | Optional | Optional when qualified source rebuild meets commitments |
| Email | Configurable SMTP | Postmark |
| Repository boundary | Public application and reusable automation | Same public components; private customer inventory and operational records |
| S3/Kubernetes | No installation prerequisite | No analytical-serving prerequisite |

Publish backup/export and rebuild procedures in the public tooling. Optional backup
configuration does not promise recovery of the only local copy after host loss.
Caddy remains the optional Compose HTTPS edge; Kamal supplies the managed edge.
Both profiles declare single-host application availability limits.

Managed environments dedicate application compute, data-service resources, and
credentials to the customer. Sharing a management or observability service does
not permit cross-customer data access. Shared infrastructure administration must
use scoped identities and preserve customer isolation.

Managed v1 assigns each customer separate application and database VPSs, scoped
deployment credentials, PostgreSQL roles and isolated local data directories.
Record database, backup bucket, provider administration and backup isolation
boundaries and reject cross-customer access. Managed service resource isolation
does not imply a physically dedicated database or storage server.

An application VPS failure or maintenance reboot interrupts that customer's
service until restart or replacement. Lost local data additionally requires
analytical rebuild or a qualified analytical restore. The separate PostgreSQL
VPS can preserve state through application-host loss, but cannot restore missing
local analytical files. Database-host loss requires PostgreSQL restoration from
off-host backups; protect against the old primary returning as an active writer.
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

### Secrets ownership and lifecycle

The target uses three secret categories. Ownership and purpose determine the
category; a customer-supplied email-provider key is domain data even when our
platform email-provider key is bootstrap configuration.

| Category | Examples | Owner and delivery |
|---|---|---|
| Deployment and operations | Provisioning tokens, registry access, deployment SSH credentials, backup repository credentials | Operators; short-lived identity or GitHub environment secrets, delivered only to the relevant job or host |
| Application bootstrap | Application PostgreSQL credential, encryption keyring, signing keys, platform email credential | Operators; protected configuration provisioned at deployment, independently recoverable |
| Customer/domain credentials | Source passwords, provider API keys, OAuth refresh tokens and integration signing secrets | Authorized application administrators; encrypted PostgreSQL records through UI/API/bootstrap |

Use a GitHub environment per customer and deployment environment. Scope secrets
and job permissions, restrict deployment refs, review and pin workflow dependencies,
and keep untrusted pull-request code outside privileged jobs. Validate the chosen
GitHub plan against required environment protections, including private-repository
approval gates. Prefer short-lived OIDC credentials where supported. Runtime,
migration, provisioning and backup privileges must not be delivered wholesale to
the application. Reusable configuration can be public; actual secret values cannot.

Kamal consumes the runner's supplied values and writes secret environment files
on the target host. Protect these files and Docker/root access; this is not RAM-only
storage. Deployed application serving and restart use the provisioned configuration
without a live GitHub or Infisical lookup. Secret updates require explicit delivery
and coordinated restart/reload. Keep an independent encrypted recovery copy of
essential bootstrap keys and document recovery when GitHub is unavailable.

Customer credential management is an application capability shared by both profiles.
UI, API and bootstrap/CLI use the same authorized, audited service for validation,
encryption and version activation. Bootstrap must not bypass it through raw SQL.
Accept secret input through protected stdin, files or authenticated requests;
exclude values from command arguments, logs, telemetry and portable analytics
definitions. Hash credentials used only for verification; encrypt external
credentials that the application must recover. Workload identities can remain
references without persisting reusable external secrets.

Use standard authenticated encryption with secure random nonces, a key identifier
and authenticated context binding ciphertext to its owner, credential and purpose.
Each deployment has a distinct versioned keyring, stored separately from PostgreSQL.
Missing keys fail closed; do not generate replacements over existing encrypted
state. Rotate keys with resumable re-encryption and retain old decryption keys for
retained backups. Test restoration of the database and keys together. Database
backup encryption and application credential encryption have separate purposes.

Credential replacement must validate and activate a version, refresh pools/caches,
and define in-flight and upstream revocation behavior. A SQL transaction alone
does not rotate every active connection. Preserve scoped authorization, version
pinning, bounded credential lifetimes and audit evidence. Separating keys from
database backups limits backup-only disclosure; privileged application/host or
deployment access can still expose decrypted credentials.

Infisical is not required by either target profile. ADR-0003 continues to describe
the security contract for an explicitly configured Infisical resolver. Record
credential record formats, key rotation and activation protocols in a focused
credential-lifecycle ADR before implementing that product contract; that detail
does not reopen the PostgreSQL storage and deployment-secret choices made here.

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
key/configuration recovery. Hetzner Object Storage is the selected managed backup
destination; self-hosters can choose a supported alternative. A second local
directory/volume is not off-host. Off-host storage at Hetzner shares provider and
account risks with compute; qualify that failure boundary and add an independently
controlled copy when required by the recovery commitment.
Test recovery, retention and deletion behavior against promised RPO/RTO. Use
pgBackRest for the operated PostgreSQL profile; verify base backups and continuous
WAL availability through actual restoration, not only successful backup commands.

Backups of genuinely rebuildable analytical outputs are optional. Use them when
they reduce recovery time or dependence on upstream availability. S3 analytical
serving remains a future separately qualified option for multiple hosts, larger
workloads or different recovery requirements, without becoming a default dependency.

### Service selection and qualification

The target stack above selects the v1 providers and tools. Launch qualification
must establish their configured behavior, versions, service plans and operational
evidence; it does not defer the selection itself:

- **Application VPS and host operations:** qualify the European Hetzner region,
  dedicated customer assignment, sizing, OS/Docker/proxy maintenance, support and
  replacement capacity. Record patch/reboot behavior, operator access, costs and
  recovery evidence. LeapView owns these duties; Kamal does not outsource them.
- **PostgreSQL:** use one customer database VPS with a qualified PostgreSQL major
  version, persistent storage, scoped roles, verified TLS and host access controls.
  Ansible and pgBackRest own repeatable configuration and recovery mechanics.
  Qualify minor updates, OS reboots, major upgrades, PITR, retention, archive
  failures and complete host replacement. Managed database providers such as
  Ubicloud are researched alternatives, not selected v1 dependencies or support
  commitments. A future change requires explicit qualification.
- **Local analytical storage:** qualify native filesystem paths and permissions,
  integrity, persistent Docker mounts, disk-full behavior, capacity and performance
  under refresh and release overlap. Test complete disk loss and catalog replacement.
- **Off-host recovery storage:** qualify Hetzner Object Storage with pgBackRest and
  retained-file recovery for European regions, tenant isolation, protected copies,
  retention, key recovery, restoration/export and cost. Match capabilities to the actual backup or upload
  contract. Immutable runtime objects, if stored there, still need their declared
  conditional-write semantics; backups need their selected tool's storage guarantees.
  An S3-compatible label or versioning alone does not establish backup protection.
- **Better Stack:** qualify monitoring coverage, alert delivery, on-call escalation,
  status communication, telemetry location, retention, access and payload exclusions.
- **Tailscale:** qualify customer isolation, operator MFA, scoped OIDC runner access,
  revocation, audit coverage and recovery access during control-service outages.
- **GitHub and Trivy:** qualify environment protections on the selected plan,
  customer-scoped workflows, immutable artifact promotion, vulnerability gates and
  exceptions, bootstrap delivery and recovery without live GitHub access.
- **Cloudflare, where enabled:** qualify SSE, upload limits, cache exclusions,
  origin certificates, trusted forwarding and data-processing requirements. Direct
  ingress remains supported for deployments that omit this optional service.
- **Postmark:** qualify SMTP/TLS delivery, sender domains, retries, bounce handling,
  scoped access and recipient/content processing. European compute hosting does
  not establish European processing by every supporting service.

The release and provider qualification results belong in a mutable companion
specification or linked delivery work. This proposal establishes required
behavior, not evidence that vendor defaults meet it. Managed-service assurance
and customer qualification require separate decisions; this deployment proposal
does not establish their legal coverage or customer commitments.

### PostgreSQL maintenance and recovery ownership

Use one qualified major version per supported release profile. Minor security and
bug-fix updates follow reviewed, scheduled maintenance; package holds must have an
explicit update procedure and overdue-patch reporting. Schedule and stagger host
reboots with pre/post health checks. Cloud-init is bootstrap, not ongoing fleet
reconciliation. Derive connection/memory budgets from the actual database VPS;
OLAP memory belongs to the separately sized application host. Omit PgBouncer by
default: session advisory locks require direct or compatible session connections,
not blanket transaction pooling.

Major upgrades use PostgreSQL's supported tools, preflight checks and a rehearsal
on restored data. Retain an independent recovery copy and stop writers until
validation completes. With `pg_upgrade --link`, starting the new cluster makes the
linked old cluster unsafe to restart; copy/qualified clone modes or independent
backups provide the required recovery boundary. Returning to an old copy after
new writes needs an explicit data-loss/reconciliation decision. No fixed seconds
or sub-minute upgrade promise is inferred from the command chosen.

Configure pgBackRest base backups, continuous WAL archiving, retention and standard
scheduling. Monitor backup age, archive failures/backlog, WAL/disk growth and
restoration evidence. Encrypt off-host backups with recoverable keys controlled
separately from storage credentials; qualify retention and deletion protection.
A completed backup or configured archive interval does not guarantee RPO during
an archive failure. Define RPO for acknowledged PostgreSQL writes independently
of accepted downtime and the freshness of reconstructed analytical outputs.

### Portability, operator access and supplier boundary

The product and reusable deployment automation must run without an external
managed database service or LeapView-hosted control plane. Customer-owned
infrastructure is the long-term deployment goal; v1 qualification covers the
selected Hetzner profile, not every host or automated BYOC onboarding.
Self-hosters control their infrastructure and access grants; installing LeapView
does not grant LeapView staff access. Where customers later delegate operations,
operator access must be explicitly scoped, auditable and revocable.

For LeapView-operated environments, routine database administration stays with
LeapView's authorized operators rather than an additional database-service vendor.
This reduces service dependencies and third-party administration, and can simplify
supplier assessment. It does not establish exclusive access to data or compliance
by itself. Hetzner remains an infrastructure supplier; assess its actual role and
contract. Hosting, backup, secrets, telemetry, email and any TLS-terminating edge
services remain in the data-flow and supplier assessment where used. Exclude
credentials and customer query/data payloads from external telemetry by default.

Document access to plaintext, encryption keys, backups, infrastructure consoles
and support channels, including exceptional access. TLS and ordinary disk/backup
encryption do not prove that an infrastructure operator cannot access data while
the application processes it. Avoid claims such as "only LeapView can access your
data" or "no subprocessors" based solely on self-operated PostgreSQL. Customer
materials must distinguish public self-hosting from delegated managed operation
and accurately state the suppliers and access controls for that deployment.

### Availability and assurance boundary

Declare this offering as a recoverable service with one application host and one
database primary per customer. Establish customer eligibility, maintenance windows, support coverage, maximum acceptable
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
become a second authority for application recovery. Use pgBackRest and supported
PostgreSQL upgrade tools rather than implementing backup or database upgrade
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
and qualification cost. We retain OS, Docker, proxy, PostgreSQL patching,
backup operation and host/database recovery work. Self-operated PostgreSQL removes
a separate database-service dependency and gives us control over its location,
maintenance and access; it also makes database incident response our duty. Public
operational interfaces need compatibility discipline, documentation and sanitized fixtures.
Temporary release overlap requires spare capacity. Single-host outages and
restart-based rollback constrain the customers and service levels supported. HA,
backup retention, optional operational services, and off-provider storage introduce
recurring costs that must be measured against the offering's service commitments.

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
11. **Credential delivery and recovery:** restart with GitHub unavailable using
    already provisioned bootstrap configuration and PostgreSQL credentials. Restore
    onto a fresh host using independently protected recovery keys; test missing,
    invalid and retired keys, retained backups, customer credential activation and
    rejected cross-scope access. An optional external resolver must separately
    prove its outage/expiry behavior. Include actual dependencies in recovery time.
12. **Analytics overload:** saturate memory, query concurrency and temporary-disk
    budgets under release overlap. Verify bounded admission and failure behavior,
    truthful readiness, and sufficient capacity for release/recovery operations.
13. **Application host loss:** provision an empty VPS, reconnect to the separate
    PostgreSQL VPS and rebuild analytical outputs into fresh metadata/files. Fence a
    returning host and preserve authoritative customer writes. Exercise unavailable
    sources, interrupted rebuild, invalid outputs, repeated snapshot numbers and
    failed/lost-ack publication. Test authoritative-state restoration separately.
14. **Service qualification:** approve the measured maintenance/recovery bounds,
    support model, customer eligibility and applicable legal/assurance controls.
    Retain evidence of isolated backups and scheduled recovery testing.
15. **Database lifecycle and loss:** provision from public automation, repeat it
    safely, patch and reboot, rehearse a major upgrade and its recovery path, then
    restore to an empty replacement with the original primary unavailable. Test
    failed WAL archiving, backup/key unavailability, old-primary fencing and
    governed application behaviour after PITR. Record achieved RPO/RTO.
16. **Data access boundary:** record infrastructure and optional service access,
    test scoped operator grants/revocation, telemetry exclusion and backup-key
    separation. Verify public installation needs no LeapView operator access.

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
- [PostgreSQL version policy](https://www.postgresql.org/support/versioning/),
  [major upgrades](https://www.postgresql.org/docs/17/pgupgrade.html),
  [pgBackRest](https://pgbackrest.org/user-guide.html), and
  [PgBouncer compatibility](https://www.pgbouncer.org/features.html).
- [Hetzner data protection](https://docs.hetzner.com/general/company-and-policy/data-protection-at-hetzner/)
  and [EDPB processor roles](https://www.edpb.europa.eu/sme/learn-the-basics/data-controller-or-data-processor_en).
- [Better Stack services and plans](https://betterstack.com/pricing),
  [Tailscale workload identity federation](https://tailscale.com/docs/features/workload-identity-federation),
  and [Tailscale GitHub integration](https://tailscale.com/docs/integrations/github/github-action).
- [GitHub environment protections and secrets](https://docs.github.com/en/actions/reference/workflows-and-actions/deployments-and-environments),
  [Kamal secret environment handling](https://kamal-deploy.org/docs/configuration/environment-variables/),
  and [OWASP cryptographic storage](https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html).
- [Cloudflare origin TLS](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full-strict/)
  and [data localization](https://developers.cloudflare.com/data-localization/).
- [Trivy image scanning](https://trivy.dev/docs/latest/target/container_image/)
  and [Postmark SMTP](https://postmarkapp.com/developer/user-guide/send-email-with-smtp).
