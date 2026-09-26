# Deployment stack research: minimize custom infrastructure code

Date: 2026-09-25

Last revised: 2026-09-26

Status: research and qualification proposal; no platform or provider is approved

Governing proposal: [ADR-0025](../0025-share-an-open-deployment-stack-for-self-hosted-and-managed-leapview.md)

Scope clarification after this research: self-hosting prioritizes a straightforward
Compose installation; operator deployments prioritize reuse and robustness. The
ADR now proposes Kamal on one dedicated application VPS per customer on European
Hetzner infrastructure, with managed PostgreSQL and local SSD analytical files.
Derived data is explicitly rebuildable where complete source replay is supported.
Off-host recovery protects irreplaceable managed customer state; S3 is an optional
recovery destination rather than a required analytical-serving dependency.
Restart-based rollback and measured downtime are accepted; a warm retained
release is no longer required. Customer-owned infrastructure remains a future option.
The alternatives below remain research context, not additional v1 support promises.

## Evaluation objective

Minimize the infrastructure software and operational procedures LeapView must
maintain over time. Count controller code, integration code, cluster operations,
recovery procedures, provider dependence, and qualification cost together.
Fewer named tools alone does not establish a simpler system.

The public Compose installation remains supported. Managed hosting uses dedicated
customer environments, with the same application images and public operational
contracts. V1 has single-host application availability. Higher availability is a
separately qualified topology when service obligations require it. Host replacement
now includes analytical rebuild from sources or an optional consistent backup.

## Findings and recommendation

Prefer established deployment controllers over implementing a LeapView controller
for slots, replica replacement, promotion, monitoring, traffic switching, and
rollback. Qualify one managed deployment path and publish its configuration.
Do not commit to supporting every candidate below.

| Candidate | Existing machinery reused | Remaining cost or limitation | Research conclusion |
|---|---|---|---|
| Stock Kamal | Remote container deployment and health-gated release switching | Stops the old container; warm retained releases require additional orchestration; host lifecycle remains ours | Selected proposed v1 path with restart-based rollback |
| Docker Swarm + Traefik | Service reconciliation, replica replacement, configurable rolling updates and failure rollback, dynamic ingress | Cluster/quorum and host operations remain ours; no built-in retained full blue/green release contract | Lightweight candidate if rolling replacement meets the service objectives |
| Managed Kubernetes + Argo Rollouts | Managed node/control-plane lifecycle plus standard rollout controllers | Provider cost and qualification, Kubernetes configuration, controller upgrades and application integration | Deferred alternative if future requirements justify advanced release analysis |
| Self-managed K3s + Argo Rollouts | Standard rollout controllers and Kubernetes APIs | We own cluster upgrades, networking, datastore recovery and failure response | Portable alternative, but does not remove cluster operations |
| Uncloud | Compose-oriented deployment, networking and Caddy integration | Failed updates can leave mixed versions; unhealthy containers are removed from routing without automatic health-triggered restart/rollback after deployment | Revisit after core release requirements are satisfied; not the leading candidate |
| Coolify / Dokploy | Operator UI/API and deployment management | Underlying deployment semantics and edition boundaries still matter | Useful administration products; not sufficient grounds to select a release engine |
| Komodo / Semaphore UI | Existing interfaces for server operations or running automation | Another privileged service to operate; not an application recovery authority | Optional replacements for a future custom operations dashboard |

Kamal's [deployment sequence](https://kamal-deploy.org/docs/commands/deploy/)
stops the prior container. Swarm supports
[service rollback and update monitoring](https://docs.docker.com/engine/swarm/services/),
but [stack deployment uses the legacy Compose v3 format](https://docs.docker.com/engine/swarm/stack-deploy/).
Reusing Compose concepts does not mean the existing file works unchanged.
Swarm [manager quorum](https://docs.docker.com/engine/swarm/admin_guide/) and K3s
[embedded-etcd HA](https://docs.k3s.io/datastore/ha-embedded) both require explicit
failure-domain design; the latter requires at least three server nodes.
[Traefik's Swarm integration](https://doc.traefik.io/traefik/expose/swarm/advanced/)
supplies routing configuration; certificate renewal and ingress availability still
need qualification on the chosen topology.

### Why the revised requirements favor Kamal

The previous Kubernetes recommendation depended on warm retained releases and
native post-promotion analysis. Accepting restart-based rollback removes those
requirements. Kamal reuses health-gated deployment while avoiding a Kubernetes
lifecycle and managed-cluster provider dependency for each customer.

The documented [rollback](https://kamal-deploy.org/docs/commands/rollback/) uses
an earlier image; local pruning and artifact/configuration retention must match
the supported window. The [proxy health check](https://kamal-deploy.org/docs/configuration/proxy/)
ends after deployment, so continuous off-host monitoring and operator response
remain necessary. Qualify SSE buffering, timeouts, uploads and drain behavior.
Normal deployment still overlaps processes and needs memory and worker fencing.

Host maintenance stays with us. Managed PostgreSQL preserves control state while
local SSD serves analytical outputs. Rebuilding a host must preserve authoritative
customer writes and republish analytical data from retained sources into a fresh
catalog/directory. Source availability and rebuild time affect recovery. Managed
backups protect irreplaceable state; they need not include every materialization.

### Local analytical storage and replaceable pipeline outputs

[DuckLake supports local filesystem data](https://ducklake.select/docs/stable/duckdb/usage/choosing_storage)
with a PostgreSQL catalog. Local reads remove remote object requests; benchmark
representative cold/warm dashboards, concurrent refresh and disk pressure before
claiming a performance improvement. Existing filesystem adapters provide a base,
not production qualification.

The proposal distinguishes storage location from data ownership. Full-source
materializations may be rebuilt; sole-copy uploads and non-replayable incremental
history cannot be discarded. Fresh catalog/file generations provide a recovery
boundary after corruption. Retain ordinary snapshots for normal refresh, and use
existing pool identities, durable jobs and serving publication for recovery.
No custom DuckLake metadata repair engine is proposed.

Default Compose needs no S3, Grafana or backup account. Operators can configure
those integrations. Managed service operations require monitoring and protection
of irreplaceable state. Optional analytical backups can shorten rebuild downtime;
if present, their catalog and files must form a consistent recovery set. The
companion specification defines the required evidence for both paths.

### Argo Rollouts for a future stricter release profile

[Argo Rollouts blue/green](https://argoproj.github.io/argo-rollouts/features/bluegreen/)
already models active/preview services, promotion analysis, rollback after failed
post-promotion analysis, and delayed scale-down of old replicas. These are the
generic mechanisms a custom warm-slot adapter would otherwise need to own.

Configure the observation and retention windows together. The documented default
scale-down delay is short; explicit delays interact with post-promotion analysis.
Verify the selected controller version under an injected late failure. Service
selector changes also do not migrate existing SSE connections. Release overlap,
traffic draining and application compatibility still need proof.

Our application checks can run as a standard
[analysis Job](https://argoproj.github.io/argo-rollouts/analysis/job/). LeapView
returns bounded results and a meaningful exit status; Argo handles progression.
Do not duplicate the rollout state machine inside LeapView. Migration execution
must remain explicitly ordered, idempotent and independently authorized; merely
placing it in a Job does not provide those properties.

If fleet reconciliation becomes necessary,
[Argo CD ApplicationSets](https://argo-cd.readthedocs.io/en/stable/operator-manual/applicationset/Generators/)
can generate applications from customer configuration and cluster inventory.
That is a candidate replacement for a custom fleet reconciler. It is separate
from Argo Rollouts and need not be introduced in the first proof. Retain explicit
per-customer version promotion; a shared template change must not accidentally
upgrade every customer. Central deployment credentials remain a fleet-wide
security boundary.

### Managed Kubernetes on Hetzner is a distinct option

These services were researched for the earlier warm-release proposal. They are
not prerequisites or selected providers for the Kamal v1 profile:

- [Syself Autopilot](https://syself.com/docs/hetzner/apalla/concepts/overview)
  documents clusters in the customer's Hetzner account and managed node OS,
  replacement and upgrades. Its [FAQ](https://syself.com/docs/hetzner/apalla/support/faq)
  describes separate management and workload clusters. Verify supported recovery,
  failure domains, access scope, maintenance control and service terms.
- [Cloudfleet](https://cloudfleet.ai/docs/introduction/getting-started/) documents
  automatic worker provisioning in the customer's Hetzner account. Verify control
  plane location, node lifecycle responsibilities, failure behavior and exit path.
- [Ubicloud Kubernetes](https://www.ubicloud.com/use-cases/ubicloud-kubernetes)
  is hosted on Hetzner infrastructure, but its page still describes preview
  availability. Its infrastructure/account arrangement must not be confused with
  provisioning into our existing Hetzner account. Confirm production availability
  and support before treating it as an enterprise candidate.

These are third-party services, not Hetzner Cloud's own managed Kubernetes
offering. Procurement and technical qualification remain open. Keep application
packaging standard and public if a future Kubernetes profile is adopted.

Provider-owned node lifecycle should replace our host provisioning and patching
automation for those nodes. Do not run Ansible and infrastructure reconciliation
against resources simultaneously owned by the provider's controllers. OpenTofu
can still manage resources outside that ownership boundary.

### Smaller platforms and administration tools

[Uncloud](https://uncloud.run/docs/guides/deployments/rolling-deployments/)
documents per-container rollback rather than whole-deployment rollback. This is
material when more than one release is partially deployed.
[Coolify](https://coolify.io/docs/applications/deployments/rolling-updates)
explicitly excludes plain Compose applications from its application rolling-update
sequence. [Dokploy Enterprise](https://docs.dokploy.com/docs/core/enterprise)
includes audit logs and additional identity/access features; assess the edition
we would actually operate.

[Komodo](https://komo.do/docs/intro) provides server, Compose/Swarm and procedure
management. [Semaphore UI](https://semaphoreui.com/) provides a UI/API for existing
automation tools. Choose one only when it removes concrete operator work. Neither
needs to be installed into every customer environment.

## Additional infrastructure code to avoid

- **Proxy discovery and certificate controllers:** use the selected platform's
  standard edge. Compose uses optional Caddy; managed Kamal uses kamal-proxy.
  Avoid multiple release-routing owners.
- **PostgreSQL backup engines:** use qualified managed backup/PITR or a mature
  native tool such as [pgBackRest](https://pgbackrest.org/user-guide.html).
  LeapView should invoke supported operations and validate the recovered state.
- **Monitoring storage and dashboards:** use standard metrics/log collection and
  Grafana, with a hosted backend where operationally appropriate.
- **A second recovery scheduler:** retain the existing PostgreSQL/River recovery
  occurrence and evidence contracts. Kubernetes Jobs or external CI can execute
  a claimed scenario without becoming another authority for its identity.
- **A general platform API:** adopt a deployment UI or GitOps controller when
  needed before creating a LeapView-specific server inventory and reconciler.

Managed cluster recovery is separate from data recovery.
[Syself's recovery documentation](https://syself.com/docs/hetzner/apalla/concepts/operations/backup-and-disaster-recovery)
explicitly assigns application data protection to the customer and describes
rebuilding workload-cluster configuration from declared state. Inventory and
recover secrets and runtime-created resources that Git cannot recreate. No
cluster service or volume snapshot establishes a coordinated PostgreSQL,
DuckLake and object recovery point by itself.

## What remains application engineering

LeapView owns schema/catalog compatibility, credential and authorization
boundaries, job fencing and idempotency, readiness and shutdown, SSE reconnection,
recovery-point consistency, object retention rules, and representative correctness
checks. Standard tools consume these interfaces. Only adapters, public declarative
configuration, and application-specific checks should be added where they suffice.

The UBDR project remains the home for release/recovery correctness and evidence.
Replacing deployment mechanics must preserve that evidence contract and requalify
the new path. A prior successful Compose qualification does not qualify Kamal.
Conversely, a new platform is not a reason to replace completed recovery ledger
and provider-handoff work.

## Bounded selection exercise

Qualify Kamal first against the revised restart-based rollback contract. The
selected managed shape is one application VPS per customer with managed
PostgreSQL and local SSD analytical storage. Keep Compose for self-hosters.
Reopen platform selection only if measured product behavior or service obligations reveal a concrete gap;
do not rebuild Argo-style warm retention and analysis around Kamal.

For the selected path, record:

1. Product-independent controller/host code removed and new integration code added.
2. Manual steps for onboarding, upgrades, failures, certificate renewal and rebuild.
3. Idle per-customer cost, peak overlap capacity, and central operating cost.
4. Exact-version upgrade; failure before and after promotion; rollback after writes;
   node loss; management outage; and fresh-environment restoration outcomes.
5. Customer isolation, operator access, provider exit and independent self-hosting.
6. Remaining ownership for OS, Docker, Kamal/proxy, monitoring and data services.

Confirm or revisit the proposed managed path from these results. Retire replaced
mechanisms only after equivalent application guarantees and recovery evidence are
demonstrated.
This research ran no deployment, benchmark, failover or restore exercise.
