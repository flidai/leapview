# Analytics development CLI contract

Status: accepted

Implementation: pending

Last updated: 2026-09-10

Owners: LeapView maintainers

Governing decision:
[ADR-0021](../0021-adopt-a-local-first-analytics-development-workflow.md)

Related: [project delivery conformance](project-delivery-conformance.md) and
[Project namespace conformance](project-namespace-conformance.md)

## Scope

This mutable implementation-facing contract defines observable local lifecycle,
source connection profiles, preview coherence, and deployment selection behavior.
The command syntax below is the intended interface, not documentation of released
functionality. Must
and must not are normative. Implementation mechanisms remain open where the
observable contract is preserved.

## Local runtime selection

Before any Docker mutation, image pull, or development-data staging, local mode
must resolve and inspect the effective Docker endpoint using Docker's actual
configuration precedence. This includes the selected context, active context,
`DOCKER_CONTEXT`, and `DOCKER_HOST`, and any supported explicit CLI override.
It must not infer locality from `LEAPVIEW_TARGET`, the word `default` in a
context name, or a published loopback port.

The implementation must document supported local-runtime transports and their
verification checks. Recognized local Engine sockets and supported Docker
Desktop local-VM endpoints may qualify. SSH, arbitrary TCP endpoints (including
loopback tunnels), and unrecognized socket proxies must not qualify merely
because they respond to the Docker API. This is a documented host-runtime trust
boundary, not a claim to detect a compromised host or maliciously replaced
trusted runtime socket. Ambiguous locality fails closed.

Every subsequent Docker/Compose operation must use the pinned, validated
endpoint. A context switch during startup must not redirect a later subprocess.
Reattachment and lifecycle commands must verify the endpoint and checkout-owned
resource identity before mutation. A changed or unavailable daemon must not
cause fallback provisioning or deletion on a different daemon.

Failure diagnostics identify the rejected context/endpoint without exposing
credentials and explain how to select a supported local runtime. LeapView must
not silently rewrite the user's global Docker context. `dev --target staging`
uses an existing LeapView target and must not provision through remote Docker.

## Local source connection profiles

### Format and identity

The intended v1 document is strict YAML containing `version: 1` and a `profiles`
map. Each named profile contains a `connections` map keyed by logical Connection
name. For example, given a PostgreSQL Connection named `commerce` in the graph:

```yaml
version: 1

profiles:
  local:
    connections:
      commerce:
        endpoint:
          host: analytics-reader.example.com
          port: 5432
          database: commerce
          tlsMode: verify-full
        credentials:
          env: LEAPVIEW_DEV_CONNECTION_COMMERCE
```

This is the intended interface, not an implemented schema. Each connection entry
contains `endpoint` and `credentials`; endpoint fields and connector-specific
options reuse the existing binding contracts. The compiler resolves the name to
the Connection's exact graph identity and derives its connector type. Unknown
names, unsupported options, duplicate YAML keys, unsupported versions, and
unknown document fields must fail with file-and-field diagnostics. Names must
not be silently case-folded or matched to a different resource.

The initial authenticated local profile uses `credentials.env` to reference one
allowlisted `LEAPVIEW_DEV_CONNECTION_<NAME>` variable containing an atomic JSON
credential bundle. It delegates to the existing development environment resolver
and its connector-specific validation; it does not interpolate secret values
into profile YAML. Public unauthenticated connections may explicitly use
`credentials: {none: true}` only where the connector contract permits it.
Exactly one credential mode is allowed. Managed connections continue to use
managed-data setup and do not require external credential entries.

Profile v1 does not add arbitrary secret providers or an implicit host credential
chain. It does not replace existing Infisical authority or permit environment
fallback when Infisical is configured. Such a configuration conflict must be
reported, not resolved by silently changing the target's resolver selection.

Publish editor-validation schemas generated from the owning contracts and test
them against runtime validation. Do not add general YAML/Jinja templating,
Python evaluation, shell substitution, or executable authentication helpers.
Inline credentials, including credentials embedded in endpoint URLs or options,
are rejected rather than copied into target binding persistence.

### File and profile selection

The default local file is `.leapview/profiles.local.yaml` relative to the selected
checkout root. `--profile-file <path>` explicitly selects another single file;
relative flag paths resolve from the invocation directory. `--profile <name>`
selects one named profile, defaulting to `local`. These flags apply to local
`dev`, not production delivery or remote `dev --target ...`.

Do not merge files from the current directory, checkout, home directory, or a
remote target. A missing explicitly selected file or named profile fails; it
does not select another profile or target. A fixture-only project may run with
no profile file. When external bindings are required but missing, show guided
setup instead of substituting sample data or inferring production credentials.

The selected profile completely specifies the external target bindings required
by the compiled resource graph; it is not an overlay on retained bindings.
Required coverage is determined by the compiler and binding contracts for the
whole graph, not only the currently open dashboard. Managed inputs and authored
connections that do not require target bindings retain their existing contracts.
Every required target-bound connection must have an explicit profile entry.
Profile v1 has no implicit or explicit adoption-by-binding-ID shortcut: reusing
an existing binding requires an entry describing its intended configuration.

Omitted binding records may remain stored, but are ineligible for new work under
the selected profile, even if enabled or previously healthy. No browser command,
watcher, background refresh, or direct candidate API may bypass this eligibility
boundary on the local runtime. Recheck coverage on graph changes and restart.
An A-to-B switch that omits a still-required connection fails before binding
mutation; it does not report B as applied or execute B with A's omitted binding.
Diagnostics may identify the retained record as a setup aid, never as fallback.

Report the resolved file, selected profile, and redacted effective endpoints
before upstream access. A profile name, even `prod`, cannot select a remote
LeapView server, change the local instance's environment, or elevate credentials.
Reject combinations of local profile flags and remote-target mode.

Bootstrap excludes the local profile file and secret files from Git by default.
Non-secret example profiles may be committed outside `dashboards/`. Neither
profiles nor secret files enter portable source bundles, deployment operation
artifacts, or generated analytics resources. Explicitly selected profile files
inside the portable source root are rejected. Profile data is not an alternative
authored Project or access resource.

### Setup, application, and connection tests

Guided setup and CLI editing must round-trip this same representation. Show the
intended file changes before overwriting existing configuration. Secret entry
must use a protected channel, never shell command arguments or echoed output.
Reuse existing binding administration and credential resolvers when applying a
profile to the local target. Generate target-owned IDs through the owning APIs;
do not persist human-readable names as replacement execution identities.

Only the selected profile's credential references may be supplied to the
validated local application runtime. No wholesale host-environment forwarding,
credential-directory mounting, production credential pull, or remote credential
upload occurs. Missing or invalid bundles fail closed. Diagnostics, logs, and
structured output redact secret values. Profile loading does not implicitly
delete existing bindings absent from the file; retention does not grant execution
eligibility or permission to retain their usable runtime credentials.

Concurrent sessions sharing a checkout must agree on the selected profile and
effective binding configuration. A conflicting attachment must fail before
mutating bindings. Switching profiles or applying endpoint changes requires an
explicit action coordinated with existing sessions and binding revision checks;
editing the profile file is not an implicit live retargeting mechanism. Any
changed execution inputs require normal planning and qualification, not mutation
of a sealed candidate or its evidence.

Connection testing runs from the actual local application container. It checks
connector-appropriate network reachability, TLS, authentication, and required
read permissions without bulk ingestion or upstream writes. Failures distinguish
DNS/VPN/routing, TLS trust, credentials, permissions, and unsupported connectors
where available evidence permits; uncertain causes must not be presented as
diagnosed facts. Passing a host-side connection test alone is insufficient.

### Complete application and interruption recovery

Before applying changes, capture the complete selected configuration and validate
its schema, graph coverage, endpoints, credential references and supplied bundles,
resolver compatibility, session ownership, and expected binding revisions as far
as possible without mutation. Connector health checks that require prepared
runtime resources remain part of application, not proof that preflight alone
has succeeded.

Profile application has observable `applying`, `incomplete`, and `applied` states.
Before the first binding or credential-state mutation, retain a checkout- and
runtime-scoped application identity with the intended configuration, exact graph
identity, required/selected connection set, and expected binding/credential
evidence. Retain references and protected comparison evidence, not secret values.
The profile file path and name alone are not application identity. This is a
local configuration checkpoint, not a second plan/build/publish state machine or
a requirement for a cross-binding database transaction.

Admission of new profile-dependent work closes before mutation and opens only
when every selected binding and supplied credential state has been reconciled
to the captured intent, every required connection is covered, and runtime health
and retirement requirements are satisfied. The gate is enforced by the local
runtime, not only by the foreground CLI. It covers new plans, builds, queries,
and refreshes; only bounded application probes and recovery actions may proceed.
Already-admitted readers may drain against their exact old evidence and current
authorization within the bounded retirement policy below. A previous rendered
view may remain visibly stale, but no mixed configuration is presented as ready.

Any partial failure or interruption leaves the application incomplete and the
gate closed, including after CLI or runtime restart. `dev status` and attachment
diagnostics distinguish the last completed application from the attempted one
and report incomplete connections without secrets. An attachment never declares
success merely because each stored binding is individually healthy. The runtime
must verify that all belong to the intended complete application; `applied` does
not itself mean a new analytics candidate has been qualified or published.

Recovery reconciles actual binding revisions, prepared/active credential evidence,
and acknowledgements against that exact retained intent, then idempotently
completes the missing work. Do not infer completion from a client progress counter
or silently recapture edited profiles, changed credentials, or a changed graph.
Missing credentials must be resupplied and matched to the intended evidence.
A revision conflict, unavailable intended credential version, or missing checkpoint
keeps admission closed and requires an explicit replacement/recovery action.
An explicit return to profile A or replacement with a revised B is another
complete application; there is no automatic rollback or silent partial adoption.
A preflight rejection before any mutation can leave the previously applied
profile available, but must clearly report that the requested profile was rejected.

### Credential agreement, rotation, and retirement

Compare supplied credentials with the runtime's applied credential state, not
just the profile YAML, variable name, or presence of a binding. For environment
credentials, use the existing resolver's exact bundle-version semantics for
each reference and compare against the applied version in a protected channel.
The existing resolver derives a version from the supplied bundle bytes, so even
different encodings must not be assumed equivalent without explicit validation.
Keep secret-derived comparison material private to the local credential/application
boundary; do not expose raw bundle hashes in diagnostics, portable artifacts,
or ordinary status output. Comparison tokens are not credentials or proof of
principal or authorization equivalence.

An attaching session with the same profile and variable name but a different
bundle fails before changing runtime state. It must not silently overwrite the
runtime's credentials or succeed using the previous session's credentials.
Unverifiable agreement after restart also fails closed until reconciliation.
The environment backing a running container does not change merely because a
second terminal exports a different value.

Supplying a replacement bundle requires an explicit rotation/replacement action
through existing credential resolver and binding operations. It is a new
application intent, coordinated with session ownership and the admission gate,
not a side effect of attachment, YAML saving, or scheduled resolver refresh.
Other attached sessions must detach before such a local configuration change;
they may subsequently attach with the new applied credential state. If the
environment-backed runtime cannot safely replace its inputs in place, explicit
local runtime recreation is allowed under those same ownership rules.

Prepare and health-check replacements before selecting their pools; preserve
exact provider versions, binding revision checks, audit behavior, and reader
leases through existing rotation mechanisms. A failed replacement is not reported
as applied. Pre-mutation failure may retain the previous applied state; failure
after mutation follows the incomplete-application gate above, even if an old pool
could otherwise remain usable under its stale policy.

Treat a secret-only rotation for the same verified principal, endpoint, role,
privileges, and result-affecting semantics separately from a principal or scope
change. Only established execution equivalence permits existing physical/cache
reuse, and normal qualification and live authorization still apply. Changed
principal or authorization scope requires renewed authorization and qualification
and invalidates incompatible cache/reuse evidence. If equivalence cannot be
established, treat the change conservatively as replacement, not harmless rotation.
An unchanged secret reference or a successful health check is insufficient proof.
These rules preserve EP-04 through EP-06 and PI-09/PI-10 in the
[delivery contract](project-delivery-conformance.md#execution-provenance-and-governance).

Switching away from a credential-bearing connection immediately prevents new
leases and credential resolution for that connection. Stop its refresh loop,
retire its pools, and remove the old resolver input. Existing authorized readers
may finish only within a documented finite drain deadline; on expiry cancel the
remaining readers and close their pools. Destroy retired credential snapshots
and temporary credential material once readers drain. Remove obsolete injected values from
runtime/container configuration, recreating or removing credential-bearing local
containers when required; merely stopping a container does not remove its stored
environment. Do not mark the replacement application complete until retirement
has completed. Restart must not rehydrate omitted credentials from old runtime
configuration. Persisted binding references and audit evidence may remain; usable
old credentials may not. This is local retirement, not revocation of the account
or secret at the upstream provider.

### Existing mechanisms and implementation boundary

The [environment resolver](../../internal/analytics/environment/resolver.go)
already scopes references, versions bundle bytes, and rejects unavailable exact
versions. [Binding administration](../../internal/analytics/connectionbinding/administration.go)
owns revision-checked changes. The
[pool manager](../../internal/analytics/connectionbinding/rotation.go) prepares
and health-checks replacement pools, records rotation evidence, and retires old
pools while leases drain. These mechanisms do not by themselves establish full
profile coverage, all-binding application completion, attachment credential
agreement, or removal of obsolete container inputs. Those integration guarantees
remain pending and require the conformance scenarios below.

### Upstream reads and retained local data

Local bindings may point to an approved upstream also used by production, but
use developer-specific least-privilege credentials, preferably read-only source
access. Network reachability and data-handling approval remain prerequisites.
Connecting locally does not require credentials from the production LeapView
instance or imply that all remote rows should be copied.

Before first upstream data access, surface the selected source and intended
read/refresh scope for explicit confirmation or an explicit automation policy.
Distinguish live reads from refresh/build work producing retained local results.
Use existing pipeline/input contracts for refresh bounds; do not invent generic
row-limit sampling or place transformation SQL in connection profiles. Tests and
metadata probes are identified separately from data ingestion.

Ordinary source-code saves do not silently refresh mutable upstream inputs.
Retained local materializations can be reused only under existing execution
identity and snapshot rules. Direct external reads still require connectivity;
local profiles do not guarantee offline operation or cross-system snapshots.
Deploy sends portable analytics source; production independently uses its own
bindings, data, and credentials.

### Optional dbt interoperability

An importer may later translate an explicitly selected, supported dbt profile
target into one selected logical local connection. Preview the mapping, require
confirmation before replacing configuration, and preserve secret-reference
handling. Reject unsupported adapter settings and unsupported expressions rather
than silently dropping them or executing arbitrary code. Imports neither push
credentials to production nor establish general dbt `profiles.yml` compatibility.
An importer is not required for initial profile conformance.

## Attached sessions and lifecycle

A live session is a CLI attachment whose ownership and liveness are verified
for one canonical checkout and managed runtime. A saved PID alone is not
sufficient identity. The implementation must define bounded crash detection;
stale records must not keep services owned forever. Uncertain ownership blocks
destructive lifecycle operations until resolved.

| Event or command | Required behavior |
| --- | --- |
| First `dev` attachment | Start or reuse this checkout's validated runtime and attach. |
| Another attachment to the same checkout | Share the runtime; register independent session ownership. |
| Ctrl-C with another live attachment | Detach only the caller; keep services running. |
| Ctrl-C of the last live attachment | Detach and gracefully stop managed services; retain volumes. |
| `dev stop` with live attachments | Refuse without mutation; identify the sessions to detach first. |
| `dev stop` without live attachments | Gracefully stop only this checkout's services; retain volumes. |
| `dev reset` with live attachments | Refuse without mutation, even if removal was otherwise confirmed. |
| `dev reset` without live attachments | Identify exact local resources, confirm removal, stop gracefully, then remove only the confirmed state. |

Session join, last-session shutdown, stop, and reset must coordinate so a new
attachment cannot be registered against resources concurrently being stopped
or removed. A join waits or returns an actionable retry result during teardown.
Reset must recheck ownership and liveness at mutation time. It must not silently
recreate deleted state through a surviving watcher. There is no implicit
all-sessions force behavior in this contract.

`dev status` reports runtime state, active attachments, and checkout ownership;
`dev logs` is read-only. Lifecycle commands never act on another checkout's
resources, even if project names match. Noninteractive reset must require an
explicit confirmation bound to the selected checkout and resource set.

## Dashboard view identity

At the beginning of a render or refresh, resolve the session pointer once and
acquire the candidate's required snapshot leases. Assign the resulting view a
revision identity. All chart, KPI, filter-option, table-page, and lazy queries
for that view use this pinned identity; none independently follows the pointer.
Existing authorization checks continue on every operation.

Cache reuse is allowed only when compatible with the pinned candidate,
snapshots, query inputs, and authorization scope. A newer candidate becoming
available starts a new view revision, not an in-place identity change for old
queries. Old leases remain protected until their readers drain.

The browser must reject responses incompatible with its current view revision,
including delayed SSE patches and cached completions. Cancelling old work is an
optimization, not the correctness mechanism. On transition, either retain the
previous complete view as visibly pending replacement, or clear obsolete
results before showing new results. Never label a mixture of candidate A and
candidate B results as one current view. Failed builds preserve the last valid
candidate and its coherent view. Explicit candidate preview URLs remain pinned
and do not acquire session-following behavior.

This contract pins LeapView candidate and managed-snapshot identity. It does
not imply snapshot isolation across external data systems that lack it.

## Deployment operation selection

An operation descriptor binds a human-readable handle to its canonical target
identity, Project/environment, immutable source snapshot and digest, and exact
delivery identities as they become known. It is durable before a mutating
request whose lost acknowledgement would otherwise lose operation identity.
Descriptors must support safe reconciliation at every interruption boundary,
including before a plan or candidate response reaches the client.

A handle selects existing delivery state; it is not a credential, approval,
mutable source alias, or new server-side deployment state machine. Handles must
not be reassigned within their documented workspace/target namespace. Concurrent
commands selecting the same operation must not advance it inconsistently.

### Interactive workflow

| Invocation | Selection contract |
| --- | --- |
| `leapview deploy --target prod` with no unresolved work | Begin the guided new-deployment flow. |
| Same command with unresolved work | Show retained operations and require a choice to resume one or start new; no automatic newest-operation selection. |
| `leapview deploy --target prod --new` | Explicit new-source intent; allocate a fresh handle and run the normal review flow. |
| `leapview deploy --target prod --resume` | Present retained operations for explicit selection and confirmation. |
| `leapview deploy --target prod --resume --operation release-42` | Select exactly that retained operation and show its identity before resuming. |

Before resumption, show the canonical destination, Project/environment, source
revision when available, source digest, creation time, and known outcome. A
dirty checkout's source digest remains authoritative when no commit describes
it. If current files differ, explain that resume uses retained source and does
not include those edits. The author need not copy plan or candidate IDs.

Selection must precede source upload, new planning, building, or publication.
Read-only reconciliation may populate the selection screen. Choosing an
operation does not substitute for target-required review and approval.

### Automation and ambiguity

Noninteractive calls must use explicit `--new` or `--resume`, together with
`--operation <handle>`. The two intent flags are mutually exclusive. For example:

```sh
leapview deploy --target prod --new --operation release-42
leapview deploy --target prod --resume --operation release-42
```

New intent with an existing handle fails and instructs the caller to resume or
choose a fresh handle. Resume with an unknown handle fails; it never falls back
to new. Missing intent, missing selection, multiple matching descriptors, or a
target identity mismatch returns a nonzero structured selection error without
advancing delivery. A mutable target alias cannot retarget a retained operation.

CI must retain the operation descriptor as an artifact when later steps run on
another machine. Import verifies the descriptor and target association; losing
the descriptor must not cause reconstruction from current files or selection
of the target's latest candidate. Handles alone do not guarantee recovery on a
fresh runner. Credentials remain in the target/client credential mechanisms,
not in exported descriptors.

### Reconciliation after selection

Resume reconciles the selected operation's exact retained identities, including
any request that might have succeeded without an acknowledgement. It does not
recapture edited source, rebuild a sealed candidate, change destination, or
silently obtain a replacement plan under an earlier approval. A stale plan
requires fresh planning and review, recorded as new work rather than silently
replacing the resumed operation's identity.

An indeterminate publication for the same destination must be reconciled before
conflicting new publication proceeds, including with `--new`. If its outcome
cannot be established, report that uncertainty and stop. Explicit new intent
does not implicitly cancel another pending operation or its approvals; normal
target policy governs conflicts and supersession. Resuming a terminal operation
reports its outcome without publishing it again.

Structured output distinguishes selection errors, failure, pending approval,
active completion, and indeterminate outcomes. It includes the selected handle
and immutable identities that are known, with no unexpected prompts in
noninteractive mode. Only confirmed activation may be reported as active success.

## Required conformance evidence

| Scenario | Required evidence |
| --- | --- |
| Remote active Docker context, `DOCKER_HOST`, or `DOCKER_CONTEXT` | Each is tested, including precedence conflicts; reject before mutations, pulls, or staging. |
| Misleading context name or loopback tunnel | Neither alone passes locality validation. |
| Supported local Engine and Docker Desktop | Both supported profiles start with documented locality checks. |
| Docker context changes during startup | All operations stay on the validated endpoint or fail without redirection. |
| PostgreSQL and object-storage sources in one local profile | Both resolve exact logical Connection identities and use their own typed endpoints and credential bundles. |
| Profile-file/name selection, missing files, or conflicting remote flags | Deterministic single-file selection; no hidden merge, target redirection, or silent fallback. |
| Invalid schema, duplicate keys, unsupported version, or secret-bearing URL | Reject with redacted file/field diagnostics before applying bindings or contacting upstreams. |
| Missing credential, unselected host secret, or configured Infisical | No credential leakage, host-environment forwarding, or fallback across resolver authority. |
| Different profiles attach to one checkout | Reject conflicting configuration before mutating bindings; no implicit retargeting of existing sessions. |
| A covers commerce/inventory; B omits still-required inventory | Reject B before mutation; retained inventory is never fallback for B, including after restart or direct API requests. |
| Graph gains a required external connection | Coverage gate prevents new work until the selected profile explicitly covers it. |
| Second binding fails after first changed; CLI or runtime crashes | Application stays incomplete across restart; no mixed-profile work or false success; exact-intent reconciliation is idempotent. |
| Crash after all changes but before completion acknowledgement | Reconcile the full intended binding/credential set before reporting applied; do not repeat or infer changes from a progress counter. |
| Recovery sees edited YAML, different bundles, missing checkpoint, or revision drift | Keep admission closed; require explicit recovery/replacement rather than silently adopting current state. |
| Same profile and variable name in two terminals, different bundles | Reject conflicting attachment without mutation or credential disclosure; restart cannot silently select either bundle. |
| Explicit secret rotation versus principal/scope replacement | Validate replacement pools and exact evidence; reuse only with established equivalence and current authorization; failed partial rotation remains incomplete. |
| Switch away from a credential-bearing connection with in-flight readers | Block new leases/resolution, stop refresh, drain within a finite deadline, remove old inputs/pools/container credentials, and prevent restart rehydration. |
| Connection works on host but not in Docker | Runtime-side test reports evidenced network/TLS/authentication failures without bulk ingestion or writes. |
| Guided setup, manual profile editing, and repeated application | Same schema and owning binding APIs; protect existing files, credentials, and binding revisions. |
| Approved remote reads and subsequent YAML edits | Explicit read/refresh scope; no unrequested mutable-data refresh or offline guarantee for live reads. |
| Local profile followed by production deployment | Profiles and credentials excluded from bundles; production bindings remain unchanged. |
| Two sessions; one receives Ctrl-C | Other session and services remain usable; last detach stops services and retains data. |
| Crash or concurrent join/last exit | Stale ownership expires; a newly registered live session is never stopped implicitly. |
| Stop/reset with attachments or a racing join | Refuse or serialize safely; no active session loses resources and no other checkout is affected. |
| Several dashboard queries for A run while B arrives | One pinned view per cohort; delayed A results cannot overwrite or mix into B, including filters, table pages, caches, and SSE. |
| A loses publication acknowledgement; files change; bare deploy runs | Present explicit choice; resume reconciles A, while new intent does not substitute edited bytes into A. |
| Multiple checkpoints or concurrent invocations | Never select latest implicitly; exact-handle selection is deterministic and same-operation advancement is safe. |
| Headless missing/unknown handle, conflicting flags, or changed target alias | Structured nonzero selection error with no delivery mutation. |
| Resume from retained CI artifact | Exact source and target survive a different checkout state; missing artifacts never trigger guessed recovery. |
| Pending approval, stale plan, or unresolved activation | Preserve target policy and report distinct outcomes; never claim active success or silently replan. |

These are implementation acceptance requirements, not assertions that current
runtime tests already cover the proposed workflow.
