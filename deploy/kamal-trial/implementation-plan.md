# LeapView public site: Kamal-first deployment and storage plan

Date: 2026-09-26 UTC
Status: implementation in progress; isolated trial only, no production qualification or migration yet. See README.md and evidence/.
This plan refines the earlier evaluation plan into delivery stages and acceptance gates. It incorporates the review corrections for production activation ordering, version-specific rollback metadata, and an isolated experimental CI route.

## Problem and goal

The original failure was image accumulation on app-leapview-site-01: website images built successfully, but the host ran out of storage and could not activate updates. Jacob now wants us to test Kamal before adopting the custom updater in PR #748.

Goal: automatic, verified updates of leapview.dev with a working local rollback and bounded disk usage, using Kamal's supported deployment/cleanup facilities wherever possible. Kamal adoption is conditional on evidence that it solves the original failure without a second large custom deployment engine.

Scope: the public website and documentation, including www.leapview.dev. Keep the CFO demo, releases, DNS, database migration, disk expansion and unrelated infrastructure outside this change.

## Research baseline

Rechecked main at 5bed13f95a6686eb276b637601ed484468bca081 and PR #748, still open at 02380df053c16db893aa545553fd0ffd92979c13. Reviewed the public-site workflows, Dockerfile, Compose/Caddy configuration, operator script and timer. Inspected Kamal v2.12.0 implementation as well as official documentation. No production inspection or mutation occurred during this research; the approximately 38 GiB/full-disk observations come from the earlier investigation and must be refreshed.

Confirmed design facts:

- Kamal can deploy an existing image with --skip-push. It provides health-based proxy switching and local rollback. [Deployment](https://kamal-deploy.org/docs/commands/deploy/), [rollback](https://kamal-deploy.org/docs/commands/rollback/).
- Normal deploy pulls before its activation lock and pre-deploy hook; routine pruning follows app boot. A pre-deploy hook is therefore too late for the first disk-capacity check. [Lifecycle source](https://github.com/basecamp/kamal/blob/v2.12.0/lib/kamal/cli/main.rb).
- Images must carry the matching service label, including externally built images. Our current authored site build lacks that label. [Validation source](https://github.com/basecamp/kamal/blob/v2.12.0/lib/kamal/commands/builder/base.rb).
- Kamal selects repository:version, while our admission/build-identity contract records an immutable digest. That needs an explicit verified mapping. [Configuration source](https://github.com/basecamp/kamal/blob/v2.12.0/lib/kamal/configuration.rb).
- retain_containers counts stopped containers, including unsuccessful ones. One retained stopped container is not automatically one successful rollback. Pruning filters image ownership by service label. [Pruning source](https://github.com/basecamp/kamal/blob/v2.12.0/lib/kamal/commands/prune.rb).
- Docker's containerd store may keep both compressed and extracted content on a filesystem separate from Docker's data directory. Physical free-space measurements matter. [Docker storage documentation](https://docs.docker.com/engine/storage/containerd/).

## Proposed architecture

Build and qualify in GitHub Actions -> admit exact image -> serialized deployment preflight -> Kamal pulls/starts candidate -> proxy readiness check -> switch traffic -> verify public build -> retain rollback and clean old versions.

Traffic: visitor -> existing Caddy for HTTPS/redirects/compression -> private kamal-proxy -> active site container on port 8081.

Retain Caddy initially. Use an explicit external Docker network connection to the Kamal network and disable host publishing for kamal-proxy in the trial. Caddy must resolve the proxy by container name on that network; do not point a container at host-loopback by mistake. Preserve Host and forwarded-protocol behavior, certificates and the www redirect. The topology is proposed until tested. Kamal supports proxy publish configuration and connects its proxy to the kamal network. [Proxy settings](https://kamal-deploy.org/docs/configuration/proxy/), [proxy implementation](https://github.com/basecamp/kamal/blob/v2.12.0/lib/kamal/commands/proxy.rb).

Steady-state target: current site image plus one verified prior version, alongside the separate proxy images. During an update, the candidate also needs space. Temporary extra images during unresolved failures or migration must be recorded and cleaned after recovery; never sacrifice the only good rollback to hit a numerical limit.

## Stage 1 — Establish prerequisites and freeze the trial

1. Leave PR #748 unmerged while the replacement is evaluated. Preserve its work as fallback evidence; do not install its controller in parallel with Kamal.
2. Select a disposable SSH-accessible environment with private Docker/containerd storage matching the production versions/backend. Inspect available capacity first. Use no live Docker socket and no production image-removal commands.
3. Pin Kamal 2.12.0, Ruby/dependencies and the proxy image used in the experiment. Record host/runtime versions and artifact checksums.
4. Confirm how GitHub Actions will reach the production host: network route, SSH identity, reviewed host-key pin, and registry pull access. The existing workflow promotes a tag and polls HTTPS; it does not establish runner SSH access. Do not weaken host verification or expose secrets in logs.
5. Record proposed config: service leapview-site, port 8081, /readyz, read-only filesystem, original non-root user, tmpfs/security settings and bounded application/proxy logging.

Exit gate: reproducible isolated environment and documented deployment access; no production changes.

## Stage 2 — Prove image and proxy compatibility

1. Add a separate `.github/workflows/site-kamal-trial.yml` in the trial PR. Use a push on the dedicated trial branch, gated on exactly one open same-repository PR at that exact SHA with a `kamal-trial` label. Reject forks and do not use `pull_request_target`. This corrects the originally proposed `pull_request` trigger: its OIDC source SHA identifies the synthetic merge commit even when checkout selects the PR head. Check out and record the exact pushed PR head SHA. This allows the trial before merging the production integration; do not call the current main-only `site-image.yml` unchanged or remove its main/current-revision guards.
2. Build with the service label into the separate experimental package `ghcr.io/flidai/leapview-site-kamal-trial`, using unique PR/head/run tags. Run equivalent build, platform, provenance, SBOM, vulnerability and site qualification checks. Invoke admission with the trial repository, the trial workflow identity and exact checked-out SHA. Use trial-only deployment credentials/environment and concurrency; include no production promotion, production host access or writes to the production image package. Retagging an old image cannot add the service label; new metadata requires a new qualified artifact. The eventual production build must pass the unchanged production provenance policy; trial evidence does not authorize production deployment.
3. Use a unique version tag bound to the admitted trial digest and configure Kamal for the trial repository, skipping rebuilding. Verify the version-to-digest mapping in CI and the actual pulled host image against the admitted platform manifest before boot. Multi-platform index digest and platform digest are different identities; handle both explicitly. Verify the started container too. Pass the selected version's admitted image reference into the site build-identity argument, but do not treat that self-reported value as proof of the running bytes.
4. Exercise deliberately changed tag/digest, missing label and wrong architecture. Every case must fail before serving the candidate. Test the supported pre-app-boot hook for host checks. [Hook lifecycle](https://kamal-deploy.org/docs/hooks/pre-app-boot/).
5. Deploy behind private kamal-proxy and test Caddy, /healthz, /readyz, /build.json, representative docs/downloads/assets and www redirect. Probe through the proxy with the actual intended Host header.

Exit gate: verified existing-image deployment works with supported configuration and narrow checks. A need to bypass admission, alter Kamal internals or silently rebuild is a failed gate.

### Preserve identity and runtime settings for rollback

Before activation, atomically write a version-specific record on the deployment host containing the Kamal version/tag, source revision, admitted index digest, selected platform manifest digest and image config ID, admission evidence, and the exact runtime command/options and non-secret environment used for that version. Record the configuration/tooling version too. Keep required versioned secret material in protected local files or an already available local secret store, with references in the record; do not put secret values in CI artifacts or logs. Separate candidate records from versions that have passed public acceptance, and preserve the last verified record until its replacement is verified. Missing or contradictory records block cleanup rather than guessing a rollback target.

For rollback, fetch the retained version's record from the host and load its settings before invoking `kamal rollback VERSION`. Set `-image-reference` and the verification hook's expected identity from that record, not from the failed candidate's environment. Revalidate the local image/container against the saved admitted identity without querying the registry. Kamal starts a new container using the supplied configuration, so merely retaining the old container does not preserve the command/environment for the new rollback container. [Boot implementation](https://github.com/basecamp/kamal/blob/v2.12.0/lib/kamal/cli/app/boot.rb).

Retain these records and their required local configuration alongside every protected live/rollback version. Their lifecycle must follow image protection; never discard the only usable rollback configuration during image cleanup. This is bounded deployment metadata, not a replacement for Kamal's deployment state machine. If a larger custom engine is required to make it reliable, report the adoption cost at the trial exit gate.

## Stage 3 — Prove storage, rollback and failure behavior

Start with retain_containers: 1 to test the requested steady-state policy. Preserve the known-good version's host-local identity/configuration record as well as the test evidence. Test native behavior before adding remedies.

| Scenario | Required result |
| --- | --- |
| Ten healthy updates with comparable distinct payloads | Correct version serves each time; obsolete payloads are reclaimed; settled space fits current + prior + fixed runtime/log overhead, without growth proportional to deployment count. |
| Three consecutive unhealthy candidates, then cleanup and a healthy update | Original live site stays available, and the known-good prior version remains usable locally. |
| Registry outage during rollback, using a fresh runner with candidate settings | Load the prior host-local record, roll back without registry access, and verify both the actual running image and /build.json report the prior revision/digest with its original runtime settings. |
| Missing/corrupt prior record or mismatched local image | Reject rollback/cleanup without replacing the live application or deleting protected recovery material. |
| Registry failure during pull and repeated same-version deploy | Live version survives, rollback evidence stays intact, and a fault-free retry works. Kamal removes the selected version reference before pulling; cover that window. |
| ENOSPC before/during pull | Active service is unaffected; no destructive cleanup of protected images; recovery instructions and a successful retry are demonstrated. |
| Kill deploy before boot, around traffic switch, and before cleanup | Discover actual live state; recover without guessing, deleting protected images or blindly releasing a lock owned by a live process. |
| Cleanup error after healthy activation | Clearly report the live application state and maintenance failure; prevent further pulls if capacity is insufficient. |
| Concurrent CI requests plus operator attempt | Only the intended deployment owns activation/cleanup; no candidate is pruned during pull or boot. |
| Caddy/foreign image/shared-layer fixtures | Unrelated services and required aliases remain intact; useful site versions still start. |

Measure both peak and settled physical usage on every backing filesystem, plus inodes, container state and identities. Derive the production reserve from measured peak download/extraction/runtime overhead with an explicit margin; do not claim an arbitrary 5 GiB always suffices. Docker/containerd garbage collection may settle asynchronously, so record the measurement interval.

If failed containers displace the good rollback, first evaluate narrow cleanup of the exact recorded failed attempt. Never run unconditional pre-deploy pruning against unknown failure state. If solving this requires a substantial custom transaction/pruning engine, stop and report that cost rather than presenting it as native Kamal functionality.

Exit gate: all scenarios pass with recorded evidence and a small, explainable integration. No claim of success based only on a happy-path deployment.

## Stage 4 — Deliver a focused integration PR

Proposed file scope (final names chosen to match repository conventions):

| Area | Change |
| --- | --- |
| Dockerfile.site or site-image.yml | Add the service label once at the canonical build location; preserve qualification. |
| Kamal config and dependency lock | Define the public-site service, proxy, runtime settings and pinned tooling. |
| site-deploy.yml | Add default-off production activation gating, then replace polling-based activation with serialized Kamal activation; retain admission, main freshness and public identity verification. |
| Small verification/preflight scripts | Host capacity, image identity, version-specific rollback records and public acceptance checks; only failure cleanup proven necessary in the trial. |
| Public-site provisioning and operator entrypoint | Bootstrap the selected runtime/network and remove conflicting updater ownership. |
| Tests and runbook | Regression cases, migration/rollback instructions, capacity policy and maintenance reporting. |

All routine automatic and manual deployments go through the same GitHub workflow/concurrency group; manual version selection must pass the same provenance/admission gates. Break-glass direct deployment requires pausing/draining CI and verifying ownership first. This avoids claiming Kamal's internal lock covers its pre-lock pulls. Do not introduce another custom lock engine unless the trial demonstrates a real requirement.

### Merge safely before enabling production activation

Introduce a repository-level `LEAPVIEW_SITE_DEPLOYMENT_MODE` with explicit `paused` and `kamal` modes. An unset or invalid value means no routine production activation. The integration PR must gate every automatic and routine manual production mutation entrypoint, including workflow dispatch and production-tag writes; it must not fall back to legacy promotion. Build/qualification jobs can continue while activation is paused. The one-time serialized operator migration in Stage 5 is a separate explicit procedure, not a workflow bypass available to routine deployments.

Set the mode to `paused` and drain previously started production jobs before merging the integration. Existing runs using the old workflow do not honor the new variable, so setting it alone is insufficient. After merge, verify that both push and manual-dispatch runs perform no production SSH mutation or production-tag write while paused. The PR merge itself must not initiate host migration. The legacy host reconciler remains the only app controller until Stage 5 explicitly stops it.

Test unset, invalid and paused modes, plus an attempted `kamal` activation on an unprepared host. Normal Kamal activation must also require host ownership/readiness evidence written only after the legacy timer/service are disabled and the private deployment and public cutover are accepted. A mode change alone cannot bypass that handover check. Use the serialized operator migration procedure while automatic activation is paused; enable normal CI only at the end of Stage 5.

Capacity preflight runs before invoking Kamal, not in pre-deploy. Normal pruning stays with Kamal. An unresolved previous deployment blocks new destructive maintenance until its state is understood. Classify failures: unhealthy candidate -> keep/restore known-good; infrastructure failure -> report retryable failure; post-activation maintenance failure -> report actual live state and stop unsafe subsequent pulls. Reuse an existing monitoring destination when available; record an unconfigured destination as a remaining operational item.

Disable the old systemd reconciler during cutover. Production-tag promotion must no longer be an independent deployment trigger. Preserve existing Caddy deployment ownership explicitly. Do not automatically resume the retired reconciler from an old operator script.

Exit gate: focused checks and required hosted CI pass; PR review and merge requirements are satisfied; the merged integration demonstrably leaves production activation paused. Supersede #748 only when the tested replacement is ready, retaining its work until migration acceptance.

## Stage 5 — Rehearse and execute the migration

1. Confirm production activation remains `paused`; refresh the exact live/rollback images, containers, disk/inodes, Caddy volumes/config and health. Plan from this inventory, not old screenshots.
2. Drain all production deployment jobs. Disable the legacy timer, wait for or stop its service safely, acquire its existing operator/deployment locks, and verify no old operator deployment remains active. Keep the serving website running. This handover must precede any Kamal mutation of the host.
3. Perform one-time targeted removal of confirmed obsolete legacy site images. Preserve live, known-good rollback, all container references and unrelated images. No broad system/volume prune or direct containerd deletion. Measure reclaimed space. If Docker cannot complete deletion at zero space, stop for a concrete recovery action rather than deleting storage files manually.
4. Through the serialized operator migration procedure, deploy a production-qualified Kamal candidate privately while automatic activation remains paused. Capture its version-specific identity/runtime record and verify the actual container. Keep the old Compose application, exact digest, command/environment and Caddy configuration available for migration rollback; this legacy version is not yet a Kamal rollback target.
5. Apply the rehearsed Caddy upstream/network change, preserving certificates. Its current configuration disables the admin API, so hot reload cannot be assumed. Measure any first-cutover interruption in staging and report it before production; steady-state health switching is a separate behavior. [Caddy reload](https://caddyserver.com/docs/command-line#caddy-reload).
6. If public checks fail, restore the old Caddy upstream/config and verify the old site. Keep automatic activation paused and clear any Kamal-ready ownership evidence. Do not automatically restart the old reconciler against a stale production tag; explicitly reconcile its desired state before any return to legacy automation.
7. If checks pass, mark the first Kamal version verified, demonstrate the rehearsed migration rollback to Compose and restoration to Kamal, and write host ownership/readiness evidence. Confirm the legacy timer/service remain disabled and old operator entrypoints cannot restart them. Only now set the mode to `kamal` and dispatch the next admitted production deployment through the shared workflow.
8. Verify that next deployment and perform a native Kamal rollback using the saved prior Kamal version's metadata; verify its public and actual image identities, then restore the intended version. Record two successful normal workflow deployments and capacity/retention results. Remove the retired Compose app and migration-only extra images only after acceptance. Leave a single app deployment controller. If acceptance fails, return activation to `paused` while resolving it.

## Definition of done

- Kamal trial and failure report are checked in with reproducible instructions.
- The trial builds from the selected PR head in an isolated package/workflow; production admission guards remain unchanged.
- Merging the integration causes no production activation; enabling it requires a completed, verified controller handover.
- Production's exact admitted revision/digest is verified at the running container and public endpoint.
- A failed candidate leaves the old site working; local rollback restores the prior image, identity and runtime configuration without registry access, including from a fresh runner.
- Ten-cycle qualification proves bounded storage; production recovers measured space and passes two normal deployments with the tested retention policy.
- HTTPS, www redirect, docs, download links and frontend assets pass acceptance.
- Capacity/cleanup failures are visible, ownership and recovery are documented, and the old updater cannot race Kamal.

## Implementation checkpoint — 26 September

The isolated lifecycle, real-image compatibility and storage-edge results are in
`evidence/README.md`. Production was inspected read-only: Docker 29.1.3/containerd
overlayfs matches the final trial; the root filesystem still has zero available
bytes and the legacy reconciler timer is active. No production mutation occurred.

Native Kamal alone does not satisfy our distinct verified rollback policy. The
trial established narrow remedies: reject foreign aliases, skip a fully verified
identical-version request, restore the selected version's runtime record during
rollback, and remove only recorded rejected attempts/redundant stopped copies
before native pruning. For an already bootstrapped proxy, supported
`redeploy --skip-push` allows public acceptance before pruning. The initial
private bootstrap and production controller handover remain separate procedures.
This changes the integration sequence to pull/start -> public verification ->
record verified version -> native cleanup, with recovery before cleanup on error.

Remaining gates are documented rather than waived: hosted-runner private SSH
access, truly separate-runner rollback, measured production capacity reserve,
and a reviewed default-off adapter enforcing shared workflow serialization and
the tested safeguards. `runner-access.md` specifies proposed Tailscale OIDC setup;
it is not an applied tailnet policy. PR #751 remains a draft trial, and #748 stays
unmerged. Full local CI was attempted but blocked by the workspace Docker bridge;
focused checks and the experimental image build/admission passed.
