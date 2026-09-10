# ADR-0021: Adopt a local-first analytics development workflow

Status: accepted

Decision date: 2026-09-09

Implementation: pending

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0007](0007-adopt-plan-driven-project-delivery.md), human-facing
command orchestration and local runtime startup only;
[ADR-0020](0020-adopt-a-postgresql-centered-target-data-architecture.md), local
analytics development topology and filesystem storage profile only

Related: [ADR-0003](0003-retain-narrow-infisical-resolver.md),
[ADR-0004](0004-defer-incremental-project-reconciliation.md),
[ADR-0018](0018-retain-project-as-the-durable-deployment-namespace.md),
[ADR-0019](0019-integrate-dbt-at-the-warehouse-contract-boundary.md),
[project-delivery conformance](specifications/project-delivery-conformance.md),
[analytics development CLI contract](specifications/analytics-development-cli-contract.md)

## Context and problem statement

LeapView supports authoring analytics in YAML, but the ordinary author must
first obtain an initialized server. The existing `leapview dev` synchronizes
source into a private candidate on an already-running target. It does not
start that target. The production Compose package expects PostgreSQL URLs,
database roles, and delivery-pool configuration supplied by an operator.

The repository's `task dev` hides much of the bootstrap, but requires a source
checkout, Go, Bun, and Task. It builds LeapView itself, provisions contributor
fixtures, and enables software-development conveniences such as the Datastar
inspector. It is a contributor harness, not the installation experience for a
person changing a dashboard or semantic model.

The distinction between software development and analytics development is
currently obscured by the shared word "dev." A YAML author should be able to
clone a project, preview changes locally, and deliver them to an existing
production instance without learning the internal bootstrap protocol or
manually carrying candidate IDs between routine commands.

PostgreSQL remains required for LeapView's control plane and DuckLake catalog.
DuckLake's analytical files require file storage, not a separate data lake
service. A local filesystem can supply that storage. Automating a local
PostgreSQL service and persistent volumes can therefore preserve the existing
authority model without adding S3, MinIO, or an embedded control-plane mode.

Research into Rill, Supabase, Lightdash, Terraform, and CLI design guidance
supports combining runnable local analytics, automated dependency startup,
optional remote previews, and guided delivery over durable plans. These are
documented precedents, not comparative usability or performance measurements.

Beyond the synthetic example, authors need to connect the local runtime to real
upstream systems, including approved sources also read by production. Existing
target bindings and the development credential resolver provide the execution
boundary, but not a convenient profile-based setup experience. A project can
need several upstream connections at once; those connections are distinct from
the LeapView server selected for deployment.

## Decision drivers

- A released CLI and Docker with Compose must be sufficient for the basic
  analytics development experience.
- First use must produce a working sample dashboard; subsequent use must
  preserve data and minimize repeated setup.
- The edit/save/preview loop must explain invalid edits and retain the last
  working result without exposing partially validated state.
- Development data must be repeatable, bounded, and visibly distinct from
  production data.
- Local source setup must support multiple approved remote inputs without
  copying production credentials or changing the portable analytics graph.
- Local, remote, interactive, and automated delivery must retain one source
  snapshot, plan, candidate, and generation state machine.
- Production targeting, approval, and completion must remain explicit while
  internal identifiers become optional inspection details for routine users.
- PostgreSQL authority, target-owned credentials, whole-project validation,
  physical reuse rules, and atomic activation must remain intact.

## Considered options

- Keep remote-only `leapview dev` and require an operator-provisioned
  development instance for every author.
- Make the contributor `task dev` harness the supported analytics onboarding
  path.
- Add an embedded local control plane and a separate publication path for
  production.
- Publish a manually configured local Compose example while leaving
  bootstrap and lifecycle orchestration to each author.
- Adopt CLI-managed Docker development with optional remote previews and a
  guided deployment command over the existing delivery lifecycle.

For connection profiles, consider adopting dbt's schema unchanged, Rill's
templated connector resources, SQLMesh gateways, dlt's layered configuration,
or a strict native profile over LeapView's existing bindings. Lightdash's dbt
profile conversion is an interoperability precedent, not an independent schema.

## Decision outcome

Choose CLI-managed Docker development as the default analytics authoring
experience. The ordinary journey is:

```sh
# Create a project with a working synthetic example.
leapview init my-analytics
cd my-analytics

# Start or resume local services, watch YAML, and open a private preview.
leapview dev

# After configuring an existing production target, review and deliver source.
leapview deploy --target prod
```

An existing checkout needs only `leapview dev` once prerequisites and its
development data configuration are available. These commands describe the
accepted end state; acceptance of this ADR does not imply they are already
implemented or available in a release.

### Local runtime ownership

The CLI owns a namespaced Compose project with two persistent services: a
version-matched LeapView application and PostgreSQL. PostgreSQL contains
separate control and DuckLake catalog databases with their existing role and
migration boundaries. Docker volumes retain PostgreSQL state and the local
analytical files, managed objects, and artifacts needed by the runtime.

The launcher automates database provisioning, migrations, local credentials,
instance and Project bootstrap, physical-pool qualification/admission, and
health reconciliation through their owning components. Bootstrap is
idempotent and resumable. Packaging and orchestration reuse the product's
contracts rather than invoking the contributor toolchain or maintaining an
independent bootstrap implementation.

The default local stack requires no external PostgreSQL, object-storage
service, secret-provider service, or analytical warehouse. Local filesystem
storage preserves immutable file ownership, candidate isolation, snapshot
seals, leases, and retention. This profile does not change production
provisioning or weaken its admission and recovery requirements.

Local endpoints bind to loopback. The launcher establishes a local browser
and CLI session without requiring the author to perform production operator
onboarding. Local session setup must not become a remote authentication bypass.
The normal released product UI is used, with the Datastar inspector and
contributor diagnostics disabled by default independently of the environment
name. `task dev` remains the workflow for changing LeapView software.

### Predictable lifecycle and target selection

Bare `leapview dev` selects local development. An ambient `LEAPVIEW_TARGET`,
previous production login, or default remote profile must not silently redirect
it. Explicit remote development remains supported:

```sh
leapview dev --target staging
```

Remote mode uses an existing authorized target and starts no local containers.
It serves teams requiring shared QA, private-network data access, or compute
beyond a laptop. A separate development VPS is optional. Remote development is
an application connection, not authorization to provision a remote Docker host.

Local mode must resolve Docker's effective endpoint, including active context
and ambient `DOCKER_HOST` and `DOCKER_CONTEXT`, and validate that it is a
supported local runtime before provisioning, pulling images, or staging data.
Unsupported or unverifiable endpoints fail closed with actionable diagnostics.
Loopback publication, a context name, or a daemon label alone is not evidence
that the daemon is on the author's machine. Docker Desktop's supported local
VM can qualify as local; arbitrary remote daemons and forwarding tunnels do not.
Subsequent operations must use the validated endpoint, not resolve ambient
configuration again and silently act elsewhere.

Local development provides `dev status`, `dev logs`, `dev stop`, and an explicit
local `dev reset`. Ctrl-C detaches the current session and stops managed services
only when no other live attached session for the selected checkout requires
them, retaining volumes. Concurrent joins and exits must preserve this invariant;
crashed sessions must eventually cease to count as live.

`dev stop` and `dev reset` refuse without mutation while live sessions remain;
the user must detach them first. Lifecycle operations are scoped to the selected
checkout and its managed resources, and uncertain ownership must not authorize
stopping or deleting resources. Reset identifies and confirms the exact local
state being removed and has no remote-target behavior. The linked CLI contract
defines observable behavior without prescribing locking or liveness mechanisms.

Separate checkouts and worktrees have isolated state and storage namespaces.
Port conflicts are resolved automatically and the actual URL is reported.
Rerunning development after interruption reuses and reconciles existing state.
Runtime versions are pinned and compatible with the CLI; upgrades that change
persistent state are explicit rather than a side effect of starting a session.

### Source ownership and preview behavior

The host CLI watches authored files, captures coherent immutable snapshots,
and sends them through the normal candidate APIs. A live YAML mount is not an
alternative server configuration or activation mechanism. The compiler still
validates the complete resource graph. A file watcher never activates shared
serving state.

One stable development-session URL follows the latest valid private candidate
in the same browser tab. Each dashboard render or refresh resolves the session
pointer once, then pins every associated query to that exact candidate and
required snapshot leases. Widgets, filter options, pagination, cached responses,
and streamed results must not independently re-resolve the pointer within that
view. A moving session pointer is a navigation convenience, not deployment or
approval identity. Page, filters, and selections are preserved where their
contracts remain compatible. Exact candidate URLs remain pinned and available
for review.

A candidate transition invalidates incompatible in-flight responses. The UI
must not combine results from the previous candidate with the new candidate as
one current dashboard. It may retain the complete previous view while loading
its replacement, or clear obsolete results. Cancellation alone is insufficient:
late responses must be rejected using the pinned view identity. This preserves
the existing immutable generation and lease contracts; it does not promise
transactional snapshots across independently changing external systems.

Invalid edits produce actionable file-and-line diagnostics in the terminal
and preview. The previous working candidate remains visible with an explicit
out-of-date indication. Obsolete work must not replace a newer valid result.
Normal output describes resource changes, progress, reuse, and the next useful
action. Detailed evidence remains accessible through inspection, verbose
output, and structured JSON suitable for CI and agents.

Code edits and data refresh are separate intents. Reuse unchanged physical
work when execution identity proves it safe; presentation-only changes must
not trigger avoidable ingestion. This does not authorize an incremental
compiler or partial publication: ADR-0004's measurement gate remains in force.

### Development data and reproducibility

`leapview init` creates a complete example with small synthetic data. Existing
projects declare their development inputs: schema-compatible CSV/Parquet
fixtures, approved immutable sample revisions, or explicit development
connections. The launcher automatically stages declared local fixtures through
managed-data APIs. It must not silently download production data, substitute
unrelated sample data, or upload fixtures to production during deployment.

Fixture selection and refresh bounds preserve the relationships required by
the models. Arbitrary row limits are not a promise of representative results.
The preview identifies sample/bounded inputs and reports missing bindings with
an actionable setup path. Changing a fixture produces a new explicit data
revision; ordinary YAML saves do not silently refresh mutable inputs.

Semantic models and dashboards stay portable across environments. Each target
owns its data bindings, credentials, grants, and policy. Dedicated development
credentials may be configured once through the local resolver; production
secrets are not pulled onto laptops or propagated from local configuration by
`deploy`. Remote preview remains available when data cannot be used locally.

Version pins and non-secret development configuration can be committed so a
teammate can reproduce the setup. Secrets, machine-specific state, and delivery
checkpoints remain outside the portable source bundle. Tooling configuration
does not reintroduce `kind: Project`, authored access/publication resources,
or environment forks of the analytics graph. Project identity follows
ADR-0018: local bootstrap resolves the appropriate issuer-owned identity, and
production delivery binds to the existing target claim without replacing it.

### Local connection profiles and remote sources

Adopt a small, strict, versioned YAML profile over existing target connection
bindings. Use dbt's separation of analytical code from connection profiles as
the primary design precedent, and Rill's multiple named connectors as the
multi-source model. This is a LeapView-native contract, not a claim of dbt
`profiles.yml` compatibility.

A profile is a named set of local source bindings. Each entry resolves a
logical Connection from the authored graph and supplies its endpoint and an
explicit credential reference. Connector type comes from that Connection;
validation reuses LeapView's typed connector and binding contracts. Profiles
must not introduce independent connector definitions, Project authority,
runtime storage topology, scheduler configuration, or a second secret system.

The selected profile fully covers required external target bindings; retained
omitted records are not execution fallback. Applying a multi-connection profile
must not admit new work or report success against a partially applied set. An
interrupted application reconciles its exact retained intent before admission
resumes. This can use an incomplete state and execution gate without a new
cross-binding transaction or delivery state machine.

Attached sessions must agree on applied credential versions as well as binding
configuration. Identical variable names do not establish credential agreement.
Rotation or replacement is explicit and preserves existing qualification,
authorization, cache, and lease rules; a principal/scope change is not assumed
to be secret-only rotation. Switching away retires the old runtime credentials
and pools with bounded reader draining, while stored binding evidence may remain.

Local means that LeapView runs and maintains working state locally, not that
its input sources must be local. The local runtime may read the same approved
upstream database, replica, warehouse, or object store as production using
developer-specific, least-privilege credentials. It connects to that upstream
directly, not through the production LeapView instance. Local isolation does
not eliminate upstream query load, network access requirements, or restrictions
on copying sensitive data onto a workstation.

Profiles remain outside the portable analytics bundle. Secret values are
resolved through existing credential mechanisms, never embedded in profile
URLs or executable templates. Strict parsing, editor schema validation, and
explicit references replace Jinja, Python, and shell evaluation. Select one
profile file without hidden cross-file merging or ambient production selection.
`dev --profile local` selects local source bindings; `deploy --target prod`
selects a LeapView destination. Local profiles cannot retarget that destination
or provision remote credentials. Production retains its own bindings and
credential resolution.

Guided setup and CLI configuration use the same profile representation and
owning binding APIs. Connection testing runs inside the local runtime and
reports reachability, VPN/DNS, TLS, authentication, and permission failures
without exposing secrets. Only explicitly selected credentials enter the
validated local runtime; the launcher must not forward the entire host
environment or silently mount credential directories.

The author explicitly chooses approved upstream reads and refresh bounds.
Direct reads still require upstream connectivity; explicit refresh/build work
can produce retained local materializations under the existing pipeline and
snapshot contracts. A profile is neither an ingestion recipe nor a guarantee
of offline operation. YAML saves do not silently refresh mutable inputs.

An explicit, reviewed import of supported dbt profile targets into individual
local bindings may be added later. It is optional interoperability, not required
for initial delivery. Unsupported adapter settings must fail clearly; import
must not execute arbitrary project code or imply general dbt compatibility.

### Guided production delivery

`leapview deploy --target prod` becomes the normal interactive delivery command.
It orchestrates capture, target-owned planning, review, build, publication,
and status reconciliation. It captures source once and shows the destination,
resource impact, removals, required physical work, reusable work, and approval
requirements before avoidable expensive work. Human confirmation or configured
automation policy authorizes proceeding with that exact plan.

The explicit `plan`, `build`, and `publish` commands remain supported for
separate review gates, CI, inspection, and recovery. Durable plan, candidate,
publication, and idempotency identities remain available and are persisted for
retry. A convenience command creates no second delivery state machine.

After interruption or a lost acknowledgement, resumption reconciles the
retained operation. It never silently captures new source, changes target,
rebuilds a sealed candidate, or replans under an earlier approval. A stale plan
requires a fresh plan and review under target policy. Noninteractive callers
receive structured results and defined completion/pending/failure semantics
without unexpected prompts.

Starting a new deployment and resuming retained work are distinct intents. The
CLI must never silently choose between them when retained work makes the choice
ambiguous, nor select the most recent of several operations. Interactive
selection identifies the retained destination and source before the author
chooses to resume, without requiring internal ID copying. Automation uses
explicit intent and a deterministic operation handle. Missing or ambiguous
selection fails without beginning or advancing delivery.

Local edits do not change a resumed operation. Indeterminate earlier publication
must be reconciled before conflicting new publication can proceed. Human-readable
handles select retained work; they are not new target authority or replacements
for immutable delivery identities. All target-owned qualification, approval,
authorization, and staleness checks still apply. The linked CLI contract defines
command selection and recovery behavior in detail.

Production independently plans and qualifies the same portable source against
its own bindings, inputs, policy, and active revision. Local candidates,
catalogs, data volumes, and approvals are not promoted across targets.
Development verification is useful evidence, not proof of production data
availability or viewer authorization. Required managed data must already be
staged to production through an explicit data workflow.

The command distinguishes active deployment, awaiting approval, failed work,
and an indeterminate outcome. It exposes a status/review URL and the next
action. It never reports a pending publication as active. Protected targets
retain independent approval and activation controls; immediate-policy targets
may activate automatically. Git and CI invoke this same contract, with
retained evidence tied to the reviewed source revision.

### Compatibility and scope

This decision amends ADR-0007's removal of `deploy` from the normal human model
and its assumption that `dev` begins with a running target. Its plan-driven
protocol, exact publication, staleness checks, target-owned credentials,
rollback, and audit requirements continue to govern.

ADR-0018 continues to own durable Project and instance-bound environment identity.
ADR-0020 continues to own PostgreSQL role boundaries, immutable files, and snapshot
leases. ADR-0004's measurement gate for incremental reconciliation is unchanged.

Changing bare `dev` target resolution requires a documented command migration,
updates to scripts that rely on ambient remote targets, and compatibility
tests. Documentation must distinguish accepted behavior from released behavior
until implementation ships. This ADR does not select a new production
installer, hosted platform, multi-project server, or production database
topology. It does not make a local Docker volume a production backup strategy.

## Consequences

Authors can preview analytics with one startup command and use an existing
production instance for delivery. Small teams can operate one production VPS
without requiring shared development infrastructure. The local runtime retains
the same PostgreSQL and immutable-delivery foundations used in production.

The product takes ownership of Docker prerequisites, image distribution,
version compatibility, portable bootstrap, persistent lifecycle, diagnostics,
and development data setup. First-run image downloads and Docker resource
usage remain real costs. Local data and policies may differ from production;
production qualification remains necessary.

Local runtime support needs explicit platform validation; an unrecognized Docker
endpoint may require the author to select a supported configuration. Authors
sharing a checkout must detach before stop or reset. Preview delivery needs
view-scoped identity and stale-response rejection, not just per-query candidate
identity. Deployment tooling must retain selectable operation descriptors and
expose ambiguity instead of guessing. These costs buy predictable data placement,
non-disruptive shared sessions, coherent previews, and recovery that cannot
silently substitute edited source.

Keeping remote-only development would preserve less client orchestration but
retain mandatory infrastructure setup. Requiring `task dev` would move build
toolchain costs onto analytics authors. An embedded control plane would create
an additional correctness and support model. A manual Compose recipe would
leave the same bootstrap and recovery friction distributed across projects.
These alternatives are rejected as the default onboarding experience.

For profiles, unchanged dbt schemas would import warehouse-execution concepts
and still need adaptation for simultaneous upstream connections. Rill-style
environment branches and templating inside portable connector YAML would
weaken the chosen source/binding separation. Lightdash-style credential upload
is not a substitute for production-owned resolution. SQLMesh gateways include
state, execution, and scheduler responsibilities that the local launcher already
owns. dlt's configuration/secret separation is useful precedent, but its layered
fallback lookup is not adopted. A native profile adds a small schema and setup
surface to maintain while reusing existing validators, bindings, and resolvers.

Delivery requires coordinated work across packaging/bootstrap, preview and
data ergonomics, and guided deployment. Implementation sequencing and progress
belong in delivery tracking, not this historical decision. Reusing proven
physical work is required where applicable, but numerical performance claims
must be supported by measurement.

## Confirmation

Conformance is demonstrated by a clean-machine authoring journey using only
the released CLI and Docker with Compose: initialize a sample, open a working
dashboard, edit a metric, repair an invalid edit, restart with retained data,
and deliver to a prepared test production target without manual database setup
or copying candidate IDs.

Lifecycle and architecture checks must cover checkout isolation, port
collisions, interrupted bootstrap, repeat startup, version compatibility,
scoped stop/reset, local-only default targeting despite ambient production
configuration, and explicit remote mode without local startup. They must also
cover ambient remote Docker targeting and context changes during startup,
concurrent session exit and join, crashed attachments, and stop/reset refusal
while sessions remain live.

Preview checks must prove stable navigation, compatible UI-state preservation,
useful diagnostics, last-valid behavior, exact candidate/query identity, and
rejection of obsolete results. Data checks must prove repeatable fixture
staging, correct schema/reference behavior, explicit refresh, safe reuse, and
absence of implicit production data or credential transfer.

Profile checks must cover multiple remote sources, deterministic file/profile
selection, typed validation, exact logical-connection resolution, missing
credentials without fallback, runtime-side connectivity diagnostics, secret
redaction, and exclusion from deployment bundles. Changing local bindings must
not mutate production bindings or silently replace another attached session's
source configuration. Explicit refresh and retained local results must remain
distinct from live upstream reads.

Preview checks must include a candidate transition while several dashboard
queries are running, proving that all associated results stay pinned to one view
and incompatible late responses cannot mix into its replacement.

Delivery checks must exercise unchanged source identity across the guided
steps, target binding, independent production qualification, approval gates,
stale rejection, resumable lost acknowledgements, and accurate pending/active
reporting. Existing whole-project and atomic-publication checks remain active.

Delivery selection checks must cover interrupted publication followed by local
edits, competing checkpoints, deterministic automation selection, and missing
or ambiguous operation handles. The
[CLI contract](specifications/analytics-development-cli-contract.md) records
the detailed conformance scenarios. Passing document structure checks does not
constitute implementation conformance.

Measure cold start with and without cached images, warm restart, and
edit-to-visible-result p50/p95 on representative hardware and datasets. Include
actual semantic, model, dashboard, and invalid-edit scenarios. Report fixture
size and network conditions separately; apply ADR-0004 before introducing
incremental compilation. Usability validation includes a new analytics author
and a teammate starting from a fresh checkout.

## Research references

Consulted on 2026-09-09:

- [Rill onboarding](https://docs.rilldata.com/): runnable local analytics.
- [Rill deployment](https://docs.rilldata.com/developers/deploy/deploy-dashboard):
  a distinct deploy/update action.
- [Rill PostgreSQL connector](https://docs.rilldata.com/developers/build/connectors/data-source/postgres):
  bounded development ingestion.
- [Supabase local workflow](https://supabase.com/docs/guides/local-development/cli-workflows):
  CLI-managed services, initialization, seeds, and persistent development state.
- [Lightdash CLI](https://docs.lightdash.com/workflow/cli/reference): named
  remote previews and separate deployment.
- [Terraform plan](https://developer.hashicorp.com/terraform/cli/commands/plan):
  interactive review and saved plans for automation.
- [DuckLake storage](https://ducklake.select/docs/stable/duckdb/usage/choosing_storage):
  local filesystem and object-storage choices, not LeapView's higher-level
  isolation or recovery guarantees.
- [Docker contexts](https://docs.docker.com/engine/manage-resources/contexts/)
  and [Docker CLI configuration](https://docs.docker.com/reference/cli/docker/):
  independent daemon targeting and configuration precedence.
- [Command Line Interface Guidelines](https://clig.dev/): progress, actionable
  errors, discoverability, and recoverable operations.

Connection-profile references consulted on 2026-09-10 using the Flid reference
library, Sourcebook, and official documentation:

- [dbt profiles](https://docs.getdbt.com/docs/local/profiles.yml): profile/code
  separation, named targets, and environment-based credential references.
- [Rill connectors](https://docs.rilldata.com/developers/build/connectors/templating)
  and [local credentials](https://docs.rilldata.com/developers/build/connectors/credentials):
  named connectors and guided local setup; environment branching and credential
  push/pull are not adopted.
- [Lightdash profile conversion](https://github.com/lightdash/lightdash/blob/35906ad9e116df59d2d59da2f58e45e9b39eaff8/packages/cli/src/dbt/profile.ts):
  explicit dbt-profile adaptation as an interoperability precedent.
- [SQLMesh gateways](https://sqlmesh.readthedocs.io/en/stable/guides/configuration/#gateways):
  typed connections within a broader execution/state/scheduler configuration.
- [dlt configuration and secrets](https://dlthub.com/docs/general-usage/credentials/setup):
  separation of configuration from secrets; layered fallback is not adopted.
