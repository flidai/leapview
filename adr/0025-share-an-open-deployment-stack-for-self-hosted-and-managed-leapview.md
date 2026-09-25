# ADR-0025: Share an open deployment stack for self-hosted and managed LeapView

Status: proposed

Decision date: pending review

Proposal date: 2026-09-25

Implementation: pending; this proposal does not establish production readiness

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0020](0020-adopt-a-postgresql-centered-target-data-architecture.md),
proposed self-hosted single-host packaging exception to the managed PostgreSQL
production baseline only; PostgreSQL authority and privilege boundaries remain
unchanged

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
  established controllers and qualified managed services.
- Make basic self-hosting possible with Docker Compose and minimal configuration.
- Operate dedicated customer environments with established tools, while keeping
  Kubernetes optional for self-hosters.
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
- **Operated by LeapView:** a Kubernetes package with Argo Rollouts, initially
  qualified on European Hetzner infrastructure. Prefer a qualified service that
  operates the cluster and nodes, so the team can focus on the application.

The proposed Kubernetes/Argo choice is subject to qualification and remains part
of this draft. Cluster provider, topology, sizing and service commitments remain
open. Swarm and stock Kamal are alternatives recorded in the research, not
additional v1 deployment paths we commit to maintain.

Architectural acceptance does not clear either profile for production. The
companion qualification specification defines two separate gates: operated-cluster
qualification and bundled Compose dependency-lifecycle qualification. Reduced
custom controller code is an architectural benefit; reduced recurring operations
must be demonstrated by the selected provider's responsibilities and evidence.

V1 includes repeatable installation, upgrades, rollback, monitoring, access, and
tested recovery using existing tooling. A customer portal, automatic signup,
billing platform, custom fleet controller, general cloud abstraction, and
automated customer-account onboarding are outside v1 scope.

Keep application images, Helm/manifests, configuration and lifecycle interfaces
public and portable. Isolate Hetzner-specific provisioning from application
packaging. Later, the same product can be operated on our infrastructure or the
customer's infrastructure, with qualification of that environment. V1 does not
promise support for untested providers or arbitrary Kubernetes distributions.

### Public software and private operational information

The public repository owns:

- The application, released container images, and versioned configuration and
  administrative interfaces.
- Compose packages, Caddy configuration, deployment adapters, infrastructure modules,
  host automation, supported orchestrator manifests, and sanitized deployment examples.
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
| HTTPS edge | Caddy | Supported HTTPS overlay; an existing trusted reverse proxy may replace it. |
| Operated release deployment | Kubernetes and Argo Rollouts | Proposed v1 operator path on European Hetzner infrastructure, subject to qualification. Public packaging; no Kubernetes requirement for Compose users. |
| Infrastructure provisioning | OpenTofu with the official Hetzner provider | Public modules for VPSs, networks, firewalls, and load balancers. Qualify existing Terraform modules and state before changing their runner. |
| Host configuration | Ansible for self-managed hosts | Public OS baseline and agent configuration where we own the host. A managed cluster's node controller owns its nodes instead. |
| Observability | Prometheus-compatible metrics, Grafana Alloy, and Grafana | Public collection configuration, dashboards, and alerts. Optional local monitoring package; managed operation uses a central backend. |
| Deployment secrets | Infisical | Reference managed secrets integration with scoped machine identities. Self-hosters can supply supported secret inputs without subscribing to Infisical Cloud. |
| Release production | GitHub Actions and GHCR | Produce immutable release images and provenance; deployments verify and promote existing artifacts. |
| Data services | PostgreSQL and qualified object storage | Retain ADR-0020's authority and privilege boundaries. Bundle dependencies for basic self-hosting; independently provision managed data services for the operated profile. |
| Initial managed infrastructure | Hetzner dedicated customer VPS environments | Public provider adapter. Select dedicated-vCPU capacity and availability topology from measured workloads and service commitments. |

Compose and a managed deployment controller are alternative owners of application
container lifecycle. They must not reconcile the same containers. Both consume
shared LeapView interfaces for initialization, compatibility, migrations,
readiness, and recovery. Publish the selected controller's configuration; do not
build a general LeapView release controller for replica replacement, traffic
switching, or warm-slot retention that established tools can supply.

Qualify the proposed Kubernetes/Argo path against the application and recovery
contracts before production adoption. The linked research records alternatives
and comparison criteria. A failure to meet those criteria requires revisiting the
proposal, rather than filling the gap with a custom general release controller.
This proposal does not require operating Kubernetes ourselves.

For Compose, Caddy forwards directly to LeapView. A managed path uses the selected
platform's supported ingress and certificate integration. Use one public edge and
one owner of release routing; additional proxies require a concrete purpose.
Qualify SSE, uploads, timeouts, client identity, draining and certificate renewal.
Private administrative and metrics endpoints must remain protected.

Ansible owns self-managed host configuration and OpenTofu owns declared provider
resources. If a managed cluster service owns node provisioning, replacement and
patching, our tools must not also reconcile those nodes.

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

This proposes a bounded single-host packaging exception to ADR-0020's managed
PostgreSQL production baseline. PostgreSQL remains authoritative; its runtime,
migration and maintenance privileges remain separate. Qualify the bundled storage
implementation against existing data contracts before advertising production
support. Development fixtures are not sufficient evidence. This draft does not
select a new storage engine or extend development-only filesystem guarantees to
production. ADR-0021 continues to govern local analytics development.

Bundled production dependencies are a distinct delivery milestone. Before that
profile ships, select and qualify the storage implementation, persistence and
retention semantics, PostgreSQL major-version upgrades, coordinated data/key
restoration, and recovery after interrupted dependency upgrades. Simple first use
does not remove ownership of these dependency lifecycles.

The Compose profile declares single-host availability limits and a supported
backup/restore procedure. It can accept external database and storage services
for operators who need them. Monitoring is optional and Caddy is the supported
HTTPS addition. The Kubernetes profile can provide stronger availability and
rollout automation without making those components prerequisites for Compose.

Managed environments dedicate application compute, data-service resources, and
credentials to the customer. Sharing a management or observability service does
not permit cross-customer data access. Shared infrastructure administration must
use scoped identities and preserve customer isolation.

Before managed launch, the companion specification must select separate customer
clusters or explicitly isolated customer worker pools in a shared cluster. For
shared control planes, document administrative access and common failure impact;
prove placement enforcement and network, data and credential isolation. Separate
namespaces on shared worker pools do not satisfy dedicated customer compute.
The selected model and its limitations must match the customer-facing offering.

Two releases on one VPS provide an upgrade mechanism, not host availability.
An HA offering requires independent application failure domains, suitable
routing, qualified session/SSE and background-work coordination, and database
availability matching the promised recovery objectives. Host placement alone
does not establish site-level resilience. A dedicated VPS also does not imply
exclusive ownership of its physical host.

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

The operated Kubernetes profile uses Argo Rollouts' blue/green strategy with
active/preview services, promotion analysis, and a defined observation and
previous-release retention window. Reserve capacity for overlapping processes
and analytical work. Configure and qualify post-promotion analysis and scale-down
together so the previous release remains available throughout the promised
rollback window. Traffic rollback does not establish background-worker rollback.
The companion specification must define job admission, draining, ownership
transfer, and fencing for active, preview and retained releases.

Compose declares its own supported interruption and restart-based rollback bounds.
It does not require active/preview services or custom blue/green machinery. Both
profiles use the same release-compatibility and recovery contracts; availability
bounds are qualified separately. Deployment-tool alternatives remain in the
linked research.

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
rollback. Recovery coordinates PostgreSQL control state, DuckLake catalog state,
referenced objects, retention, and required keys. A recoverable point must remain
valid despite object garbage collection. Provider snapshots are supplementary;
restoration into a fresh environment is the evidence of recoverability.

### Service selection and qualification

Provider choices below remain candidates, not commitments made by this ADR:

- **Managed Kubernetes:** qualify European Hetzner availability, the selected
  customer isolation model, supported Argo Rollouts versions and permissions,
  and explicit ownership of control-plane upgrades, worker patching/replacement,
  networking, ingress, certificates, controller maintenance and cluster recovery.
  Record support/escalation, maintenance behavior, cost and recovery evidence.
  This gate must pass before managed launch; selecting Kubernetes does not prove
  that a provider owns these duties. Unassigned duties require a qualified owner.
- **PostgreSQL:** evaluate Ubicloud on Hetzner for version/privilege compatibility,
  connection security, replication mode, failover behavior, support, retention,
  and independently usable backup export.
- **Object storage:** use AWS S3 as a behavioral reference and evaluate Scaleway
  Multi-AZ as an alternative. Measure correctness, scan latency/throughput, and
  transfer cost from Hetzner. Do not select solely on an S3-compatible label.
- **Hetzner Object Storage:** its documented exclusion of conditional PUT/DELETE
  on versioned buckets prevents assuming it meets the combined immutable-write
  and versioned-recovery contract. Require provider qualification before adoption.
  Apply the same conditional-write scrutiny to an S3 infrastructure-state backend.
- **Operational services:** evaluate Grafana Cloud and Teleport against monitoring
  and privileged-access requirements. Their hosted products are not prerequisites
  for the public deployment stack.

The release and provider qualification results belong in a mutable companion
specification or linked delivery work. This proposal establishes required
behavior, not evidence that vendor defaults meet it. Managed-service assurance
and customer qualification require separate decisions; this deployment proposal
does not establish their legal coverage or customer commitments.

### Managed operations without a prerequisite platform build

Managed production requires inventory, repeatable provisioning, controlled
releases, monitoring and incident response, tested recovery, scoped access,
credential rotation, and an audit trail. Initially these may be delivered through
versioned configuration, CI workflows, the public tooling, existing operational
services, and documented operator procedures.

A custom fleet API, dashboard, billing integration or Temporal workflow service
is outside v1 scope. Kubernetes/Argo is the proposed operator deployment toolchain,
not a new product for customers to learn. Add further orchestration only when
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
and qualification cost. Outsourced node operations add provider dependence and
service fees; self-managed orchestration retains cluster operating work. Public
operational interfaces need compatibility discipline, documentation,
and sanitized fixtures. Overlapping release slots require spare capacity. HA,
backup retention, managed services, and off-provider storage introduce recurring
costs that must be measured against the offering's service commitments.

A bundled self-hosted database/storage profile adds upgrade, persistence and
recovery qualification duties. Its simpler installation cannot inherit the
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
2. **Shared lifecycle:** Compose and the selected controller use the same
   application artifacts and lifecycle contracts, with a single container owner
   per installation.
3. **Upgrade and rollback:** under queries, SSE, uploads, and background work,
   upgrade A to B, commit new writes on B, then roll back to A within the supported
   window. Verify preserved writes, correct authorization, reconnect behavior,
   fenced work, and each profile's documented interruption bound. Kubernetes
   additionally proves retained-release traffic rollback and worker ownership.
4. **Recovery:** restore control state, catalog, objects, and required configuration
   into fresh infrastructure. Verify representative queries and writes, and measure
   actual recovery time and data loss against the selected objectives.
5. **Provider and network behavior:** exercise concurrent immutable object creation
   with versioning, data-service privileges, TLS identity verification, certificate
   renewal, private-network restrictions, and protected administrative endpoints.
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
9. **Management interruption:** stop the runner or rollout controller during an
   update. Reconcile safely after recovery without duplicate migrations or job
   effects, unintended promotion, or premature previous-release retirement.
10. **Missing analysis evidence:** inject unavailable, empty, stale and inconclusive
    rollout metrics. Promotion remains blocked; post-promotion behavior follows
    the bounded abort/escalation policy in the companion specification. Missing
    evidence must never be interpreted as a successful check.
11. **Secrets outage on restart:** restart an application while its configured
    secret service is unavailable. Prove the declared startup/recovery behavior,
    including credential expiry and denial; fail closed when credentials cannot
    be obtained through an approved path. Include any resulting delay in recovery
    measurements rather than claiming unconditional restart availability.
12. **Analytics overload:** saturate memory, query concurrency and temporary-disk
    budgets under release overlap. Verify bounded admission and failure behavior,
    truthful readiness, and sufficient capacity for release/recovery operations.

No new qualification is claimed by drafting or accepting this ADR.

## Research references

Official documentation reviewed on 2026-09-25; provider capabilities must be
rechecked when qualifying an implementation:

- [Argo Rollouts blue/green](https://argoproj.github.io/argo-rollouts/features/bluegreen/)
  and [analysis behavior](https://argoproj.github.io/argo-rollouts/features/analysis/).
- [Kubernetes tenancy boundaries](https://kubernetes.io/docs/concepts/security/multi-tenancy/)
  and [resource management](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/).
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
