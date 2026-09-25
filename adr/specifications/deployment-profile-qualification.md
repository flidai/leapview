# Deployment profile qualification

Status: proposed requirements; no profile is qualified by this document

Date: 2026-09-25

Related: [ADR-0025](../0025-share-an-open-deployment-stack-for-self-hosted-and-managed-leapview.md),
[supporting technology research](deployment-stack-reuse-research.md)

## Purpose and release gates

Make the two deployment profiles' promises precise without implementing a custom
deployment controller. Use existing tooling, shared LeapView lifecycle commands,
and the UBDR occurrence/evidence contracts. Linear tracks milestone owners and
delivery status. This specification records the required decisions and proof.

| Gate | Required before | Current decision state |
|---|---|---|
| Operated cluster and provider | Launching managed Kubernetes deployments | Provider, topology, responsibilities and evidence unresolved |
| Bundled Compose dependencies | Advertising the bundled single-host production profile | Storage implementation and dependency lifecycle evidence unresolved |

Acceptance of the architectural direction does not satisfy these gates. Record
the actual release, configuration, provider, topology, reviewer and dated evidence
for each result. No target duration or customer promise is invented here.

## Operated cluster qualification

Select a provider available on European Hetzner infrastructure. Record worker,
control-plane, backup and relevant operational-data locations, account ownership,
customer isolation, supported Kubernetes/Argo versions, and service costs. Verify
the ability to install and operate Argo Rollouts with its required CRDs and RBAC.

For every responsibility below, name one accountable party, the implementing
tool/service, maintenance procedure, failure escalation and recovery evidence:

| Responsibility | Required coverage |
|---|---|
| Control plane | Availability, upgrades, configuration recovery and access |
| Workers | OS patching, replacement, drain behavior, capacity and placement |
| Networking | CNI, cross-node encryption where required, network policy, DNS and load balancing |
| Public edge | Ingress lifecycle, certificates, renewal, trusted client identity and SSE |
| Controllers | Argo/other installed controller versions, permissions, upgrades and recovery |
| Persistent state | Cluster configuration, runtime-created objects, secrets and independent application recovery |
| Support | Hours, escalation, maintenance notice/control, provider outage behavior and exit procedure |

Do not infer ownership from the phrase managed Kubernetes. A provider may leave
ingress, controllers, secrets or backups to us. Explicitly accept and qualify that
work or select a service that owns it. Our automation must not reconcile resources
also owned by the provider's controllers.

### Customer isolation selection

Select and document one model before launch:

- **Separate cluster per customer:** record customer-specific worker resources,
  cluster access, management-service access and provider/control-plane overhead.
- **Shared cluster with dedicated customer worker pools:** record scheduling
  enforcement, restrictions on cross-pool placement, shared administrators and
  controllers, network policy and shared control-plane failure impact.

Shared workers with namespaces alone do not satisfy the dedicated-compute offering.
For either model, specify database/storage isolation and all shared system
components. Exercise rejected cross-customer scheduling/access and a customer
overload or failure. Record whether it affects another customer's service.

The specification intentionally leaves the model unresolved until reviewed
against the offering; an implementer may not silently choose the cheaper model.

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

| Behavior | Compose | Operated Kubernetes |
|---|---|---|
| Update mechanism | Documented Compose reconciliation | Argo Rollouts blue/green |
| Availability | Measured maintenance/interruption bound | Measured cutover/drain bound with overlapping releases |
| Prior release | Retained immutable artifact and supported restart procedure | Running prior replicas for the declared observation/rollback window |
| Rollback eligibility | Shared compatibility policy | Same compatibility policy |
| Durable state | Current compatible data; explicit recovery for incompatible transitions | Same contract; traffic rollback does not restore data |

### Analysis and release retention

Pin the Argo version and record active/preview Services, readiness probes,
pre/post-promotion analysis, queries, thresholds, retries, timeouts, observation
window and previous-release scale-down settings. Configure these jointly; retaining
an image or finishing a readiness probe is not proof of the whole observation
window.

The proposed safety policy is:

- Before promotion, missing, stale, empty or inconclusive required evidence blocks
  promotion. After bounded retries or review time, abort the candidate and alert.
- After promotion, failed checks or an inability to establish success within the
  bounded observation policy abort to the compatible retained release and alert.
  An inconclusive result may pause within that bound; it cannot mark success.
- The old release must remain ready through the observation/abort deadline and
  the qualified reconciliation allowance. Test native controller behavior when
  analysis, controller downtime and scale-down deadlines interact.
- Unknown data is never zero errors. Low-traffic deployments use explicitly
  defined representative checks rather than an empty query result as success.

Map this policy to standard controller settings and supported analysis checks.
If the selected version cannot satisfy it, revise the configuration or proposal;
do not implement a parallel rollout controller. Qualify late failures and lost
metrics after traffic has switched, plus runner/controller interruption before
and after promotion.

### Background work

The release specification must record which processes may claim each mutating job
kind during preview, promotion, observation, rollback and final retirement.
The default requirement is that preview and retained HTTP releases do not claim
mutating background work. Transfer active work authority through existing durable
claims/fencing and drain or recover in-flight jobs safely. A separately deployed
compatible worker role is also possible if its lifecycle is explicitly specified.

Do not infer job ownership from the HTTP Service selector. Prove that a stale
worker cannot commit an effect after ownership transfer and that candidate
startup, rollback and lease expiry do not duplicate scheduled work. Preserve
compatibility of queued work across the supported release pair.

## Targeted failure evidence

- **Migrations:** interrupt/retry migrations; retain one durable operation and
  correct schema/catalog state. Serving identities cannot assume migration roles.
- **Management outage:** stop the runner and controller separately during rollout;
  recover through standard reconciliation without manual state rewriting or
  duplicate effects. Include full cluster-control-plane outage where promised.
- **Secrets outage:** restart with the secret service unavailable, then repeat
  with expired/revoked credentials. Document bounded cached/projected credential
  use if permitted; never bypass revocation or access controls. If startup must
  wait for the service, record that dependency and its effect on recovery time.
- **Analytics overload:** exercise query admission/concurrency, DuckDB memory,
  container memory and temporary-disk budgets during release overlap. Bound spills,
  out-of-memory behavior and retry pressure. Readiness must report the real serving
  state; operator/recovery capacity must remain sufficient to stop or recover work.
- **Restoration:** reconstruct both profiles from declared inputs without the old
  host, validate representative governed reads/writes and restart protection.

## References

- [Argo blue/green configuration](https://argoproj.github.io/argo-rollouts/features/bluegreen/)
  and [analysis outcomes](https://argoproj.github.io/argo-rollouts/features/analysis/).
- [Kubernetes tenancy](https://kubernetes.io/docs/concepts/security/multi-tenancy/)
  and [resource controls](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/).

These requirements are design commitments proposed for review, not claims that
the current application, provider or controller configuration already passes.
