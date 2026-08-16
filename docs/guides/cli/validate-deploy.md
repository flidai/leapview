# Develop, review, and publish

LeapView has one authoring lifecycle: `login → dev → publish`. It is the same for a localhost evaluator, a hosted service, a self-hosted installation, and an air-gapped target. The CLI always sends an immutable project candidate to an already-running LeapView target; it never starts a hidden local runtime or publishes by copying repository files into a server.

## Before you begin

An operator must bootstrap the target before an author uses it. The operator owns installation, target identity, HTTPS, identity-provider setup, backups, connection bindings, and credential-provider access. The author receives a target URL and an ordinary LeapView identity, not source credentials or secret-provider configuration.

For a local single-node evaluation, follow [Installation](/docs/installation) to start the evaluator and seed its bundled synthetic data. Treat that as a separate operator bootstrap step. After the target is healthy and the temporary administrator password has been changed, use the same authoring commands shown below against `http://localhost:8080`. Local evaluation relaxes approval policy and HTTPS only inside the loopback boundary; it does not introduce another authoring workflow.

Before authoring:

1. Confirm the intended target is healthy and reachable from both the CLI and browser.
2. Confirm the project entrypoint is `dashboards/leapview.yaml`.
3. Ask the operator to provision logical connection bindings and representative access grants.
4. Install a CLI release compatible with the target's advertised API contract.
5. Keep the project path and target unchanged from login through publication.

The security identities remain deliberately distinct:

| Identity | Responsibility |
| --- | --- |
| Author | Synchronizes and reviews a private candidate; sees data only through effective grants and row-level security. |
| Viewer | Consumes active target-hosted dashboards and never receives authoring privileges. |
| Publisher | Requests publication of the exact reviewed candidate. |
| Approver | Independently accepts or rejects a protected publication; cannot be the requesting publisher. |
| Operator | Bootstraps and operates the target, bindings, providers, recovery, and retention. |
| Automation | Uses a scoped service principal to call the same candidate and publication APIs. |
| Runtime | Resolves target-owned bindings and executes governed queries without impersonating an author. |
| Source | Is the external system identity held behind a target binding, never a LeapView user or CI identity. |

## Authenticate to the target

Sign in before starting the candidate loop:

```sh
export LEAPVIEW_TARGET=https://dash.example.com
leapview login "$LEAPVIEW_TARGET" \
  --project dashboards/leapview.yaml
```

`login` discovers the canonical target origin, immutable instance identity, environment, and released API contract before it creates a project-scoped CLI session. Human login uses the browser/device flow. SSO authentication proves identity but does not grant authoring, publishing, approval, source access, or data-policy bypass.

CLI OAuth credentials and browser or LeapView Desktop sessions are separate security domains. The CLI stores rotating credentials in the operating-system credential store. A target-hosted browser, including a LeapView Desktop profile, uses an HttpOnly target session. Their client registrations, storage, expiry, logout, and revocation are independently revocable and are never interchangeable.

For an air-gapped target, the CLI workstation and approval browser must be able to reach the target's private canonical origin; the lifecycle does not require public Internet access.

## Validate the project

Validate the complete resource graph before synchronizing:

```sh
leapview validate --project dashboards/leapview.yaml
```

Validation is a credential-free preflight within the same lifecycle, not a second runtime or deployment path. It checks project structure and references but cannot prove target bindings, access policy, source availability, or rendered behavior.

## Create and review the private candidate

Start the authoring loop against the authenticated target:

```sh
leapview dev \
  --project dashboards/leapview.yaml \
  --target "$LEAPVIEW_TARGET"
```

`dev` uploads content-addressed source files, compiles them on the target, resolves target-owned connection evidence and managed-data pins, and creates an owner-isolated private candidate. Local checkpoints contain only candidate identity and digests. Project YAML, author workstations, and CI never receive source credentials, Infisical references, or provider bootstrap material.

In production, the target returns a canonical-origin, token-free HTTPS URL; the loopback evaluator uses the same URL shape over local HTTP. `dev` opens it in the system browser by default. Use `--no-browser` only on a headless workstation and open the printed URL in an authenticated browser. The preview does not require LeapView Desktop, and Desktop is not an authoring client; browsers and Desktop may only consume the same authenticated target-hosted page.

Review the candidate using the author principal's real effective grants. RBAC, row-level security, column policy, and project-resource policy apply exactly as they do elsewhere. A developer does not receive a production viewer's data visibility merely because the candidate is private. Use separate representative verifier identities when a change must be qualified across several data-policy populations.

Run `dev` again after every source edit. `--once` performs one synchronization for trusted automation, but it calls the same Project, Access, Release, and Deployment APIs as the interactive loop.

## Resolve target-owned data safely

Projects name logical connections. Each target owns the physical endpoint, options, and credential reference, so the same candidate can be evaluated without distributing source credentials to developers.

Production composition supports one authoritative read-only Infisical resolver. It fetches an atomic credential bundle during candidate preparation and resolves the exact retained provider version again when an active generation starts. Provider values are not persisted in project artifacts, binding records, release provenance, or deployment evidence. A development or evaluation target may explicitly select the local environment resolver. That resolver is target-side, accepts only its dedicated development variables, and is never a fallback after an Infisical denial or outage.

Credential rotation validates a replacement pool, then requires a new candidate and publication to pin the replacement version to a serving generation. Use a fresh candidate key when the project bytes and source revision are unchanged. A bad provider version cannot become ready and does not replace the active generation. Provider outage prevents cold activation and restart from resolving the pinned version; an already open generation can continue only according to its runtime and stale-pool policy. A cold target with no validated pool always fails closed.

Static credential rotation cannot terminate already-authenticated source sessions inside an external system. Keep source session lifetimes bounded and use provider- or source-native emergency revocation when immediate termination is required.

The initial enterprise scope intentionally defers provider fallback, a built-in vault, provider writes, dynamic leases, Kubernetes integration, and additional provider adapters. Production never silently falls back from Infisical to local environment values.

## Publish the reviewed candidate

Publish with the same project path and target:

```sh
leapview publish \
  --project dashboards/leapview.yaml \
  --target "$LEAPVIEW_TARGET"
```

`publish` loads the exact candidate handoff produced by `dev`; it does not reread or rebuild the project, and it does not upload source again. Immediate-policy targets wait for activation. Protected targets persist a pending request bound to the candidate revision, release provenance, plan, policy snapshot, exact provider-version connection evidence, managed-data pins, and base generation. A separately authorized approver makes the decision on the target.

Retries are idempotent. Reuse the same candidate and immutable revision after a lost response; do not rebuild from a moving source ref. Git and CI are integrations with these same APIs, not alternative user workflows or sources of truth.

## Verify the active deployment

After activation:

1. Confirm the deployment evidence names the reviewed candidate, artifact, target, principal, and source revision when present.
2. Confirm the expected serving generation is active.
3. Exercise one catalog page, one representative semantic query, one filtered dashboard, and one affected refresh or table window.
4. Repeat data checks with representative viewer identities so row-level security is tested independently from author access.
5. Inspect deployment, access, query, binding-health, and audit evidence for rejected or degraded work.

A failed candidate or activation leaves the last valid serving generation active. Retained failed versions support diagnosis; they never become serving state implicitly.

## Recover from enterprise failures

| Failure | Expected behavior | Recovery |
| --- | --- | --- |
| Released CLI/server incompatibility | Discovery rejects the operation before candidate mutation. | Install a compatible released CLI or upgrade the target through the operator workflow. |
| SSO or CLI access expiry | A revocable CLI session refreshes once; revoked or expired session families fail authentication. | Run `leapview login` again after confirming identity-provider health and revocation intent. |
| RLS or grant rejection | Preview and verification fail without widening visibility. | Inspect effective grants and policy inputs; never give the author source credentials or impersonate a viewer. |
| Candidate preparation failure | The candidate remains private and retryable; active serving state is unchanged. | Correct the project or target binding and retry the same candidate when safe. |
| Approval expiry or denial | No activation occurs. | Request a new decision for the unchanged candidate or create a new candidate for changed content. |
| Activation or verification failure | The deployment retains exact evidence and the last valid generation remains available. | Stop promotion, diagnose the failed generation, and invoke the target's governed rollback operation if activation completed. |
| Provider outage | Health becomes degraded; a validated pool serves only within bounded stale policy. | Restore provider access or rotate to a validated version before the stale boundary. |

Rollback is a target operation over retained immutable generations. It is not a Git checkout, a project rebuild, or permission to publish a different candidate. Verify the restored generation and representative queries after rollback.

## Capability ownership

The modular monolith keeps the user lifecycle singular while preserving domain ownership:

| Capability | Owns | Dependency direction |
| --- | --- | --- |
| Access | Human CLI sessions, workload identities, RBAC, RLS inputs, grants, and audit identity. | Foundation contract; it does not depend on Project, Release, or Deployment. |
| Project | Local source snapshots, compilation inputs, dev-loop orchestration, and non-secret candidate handoff. | Depends on public analytical, project-resource, access, refresh, and serving contracts; never on Deployment adapters. |
| Release | Immutable artifact and provenance identity. | Depends on Project and serving contracts; never on Deployment. |
| Deployment | Private candidates, approval policy, publication requests, activation orchestration, retry, and rollback. | Depends inward on Access, Project, Release, Serving State, Managed Data, and Runtime Host contracts. |
| Analytics | Logical target bindings, Infisical and explicit development resolver adapters, pool rotation, and governed query execution. | Exposes contracts; provider adapters remain internal to Analytics. |
| Runtime Host | Prepared generations, candidate runtimes, snapshot leases, cutover, and drain. | Depends on Managed Data and Serving State; never on Access or Deployment. |
| Application CLI | Composes generated capability clients into `login`, `dev`, and `publish`. | Composition root only; it owns no domain state. |

Architecture tests enforce capability ownership, public-contract imports, and the absence of reverse dependency edges. The localhost evaluator and protected enterprise target compose the same Project CLI and public commands; only target policy and adapters differ.

## Troubleshooting

If `login` fails, verify canonical origin, released CLI/server compatibility, SSO reachability, clock synchronization, and revocation state. If `dev` fails after local validation, inspect target authorization, logical binding health, managed-data availability, and candidate diagnostics. If the browser cannot open, rerun with `--no-browser` and open the printed target URL manually.

If `publish` cannot find a candidate, use the same project path, target, and candidate key used by `dev`. If it is pending, do not grant the publisher approval privilege; ask an independent approver. If verification fails after activation, preserve evidence and use the governed rollback operation rather than editing serving pointers or deleting retained versions.

## Next steps

For unattended integration, continue with [Automation and CI](/docs/cli/automation). Operators should continue with [Production configuration](/docs/guides/operate/production-configuration), [Connections and sources](/docs/concepts/connections-sources), and [Self-hosting](/docs/guides/operate/self-hosting). Exact current flags are generated in the [`login`](/docs/cli/login), [`dev`](/docs/cli/dev), and [`publish`](/docs/cli/publish) references.
