# LeapView Public-Site Kamal Completion Plan

Updated: 28 September 2026  
Status: Revised after scope and recovery review; implementation pending.

## 1. Goal and scope

Finish the permanent migration of `leapview.dev` to operator-run Kamal deployments, with reliable rollback and bounded image storage. Automatic deployment remains deferred, following Jacob’s instruction.

The chosen flow is:

```text
Build and qualify a production image in GitHub
→ operator verifies the immutable image
→ check ownership, recovery state and capacity
→ pull the exact public digest
→ Kamal starts and health-checks the candidate
→ switch traffic and verify the public website
→ retain current + distinct verified prior
→ clean up obsolete versions
```

Use the verified public-pull approach selected by Ganesh. No new registry credential is required. Kamal continues to own application boot, readiness-based switching, rollback and native pruning.

Include only the public website/docs VPS, its Caddy configuration, deployment tooling, tests and operational documentation. Exclude the CFO demo, DNS changes, disk expansion, application releases, new CI network identities and automatic deployment activation. Do not install the alternative controller from #748 alongside Kamal.

### Verified starting point

- On 28 September, the public site was healthy on the original Compose/Caddy deployment, with approximately 32.3 GiB free.
- The legacy updater was disabled, and no permanent Kamal handover marker existed.
- Manual testing passed temporary public cutover, unhealthy-candidate rejection, healthy deployment, local rollback and retention. The original website was restored afterward.
- #748 is the approved fallback; #751 contains trial evidence; #752 contains the unfinished production integration.
- Main had advanced to `8283839939dd630e2a1874927c62fdb8ccadfa8d`. Refresh this baseline before implementation.
- Normal hosted checks were skipped on the draft Kamal PRs. Previous full local CI attempts failed because the shared Docker bridge was missing.

## 2. Complete the manual deployment implementation

### Operator interface and image admission

Update the existing implementation under `deploy/kamal-site`, preserving useful identity, runtime and recovery checks. Provide these commands:

| Command | Required behavior |
| --- | --- |
| `status` | Report actual application/proxy state, recorded identity, rollback availability, unresolved work and capacity. Remain usable when the application is stopped or readiness is incomplete. |
| `prepare --image <immutable-reference>` | Run the existing live OCI admission verifier and produce a protected, version-specific deployment record. |
| `deploy --record <path>` | Validate the prepared record and deploy its exact image through the guarded operator path. |
| `rollback` | Restore the recorded verified prior version using local recovery material, without contacting the registry. |
| `maintain` | Complete interrupted cleanup after verifying exclusive ownership and resolving any uncertain in-flight work. Never deploy a new candidate. |

Preparation must verify the canonical production repository, successful production image workflow, source revision, index/platform/config digests and qualification evidence. Capture release metadata from that exact source revision. Do not treat an arbitrary edited JSON admission claim as verification, and reject trial-package images in production commands.

Use the working Infisical SSH key, Tailscale route and reviewed SSH host fingerprint. Introduce explicit operator execution rather than requiring fabricated `GITHUB_*` variables.

### Image download and activation

Replace the failing `redeploy --skip-push` path with the following sequence:

1. Acquire exclusive operator access and validate admission, recovery state and capacity.
2. Persist the pending attempt before any image mutation.
3. Pull the exact public digest using Docker, then create its digest-derived host-local tag.
4. Verify the local image and selected platform against the admitted record.
5. Invoke supported `kamal app boot` with the saved runtime configuration and version.
6. Verify the actual running container and public website before recording acceptance.
7. Preserve the distinct verified prior version, remove only recorded failed/duplicate containers, invoke native pruning, and verify the result.

Do not publish deployment tags back to GHCR. Remove placeholder registry credentials and avoid registry-login commands on this path. A request for the already-active image must verify identity and health and then return without pulling, restarting or pruning.

Kamal exposes application boot and rollback commands. Its pinned build-pull implementation performs the remote registry login that caused the draft integration to fail: [app commands](https://kamal-deploy.org/docs/commands/app/), [build-pull implementation](https://github.com/basecamp/kamal/blob/v2.12.0/lib/kamal/cli/build.rb).

### Exclusive ownership and interrupted-operation recovery

Hold the host’s existing reconciliation and deployment `flock` locks from preflight through pull, activation and maintenance. Retain Kamal’s activation lock as well. Competing operator commands must fail before pulling or changing state.

**A separate SSH lock session is not sufficient proof that all remote work has stopped.** Implement these additional requirements:

- Give every attempt an identifier and record its controller identity and remote mutating-command ownership. Track remote work through completion; losing a client process must not make an executing command appear finished.
- On lock-session loss, stop issuing commands and preserve unresolved deployment or maintenance state. Do not automatically take over, clear pending state, or release a Kamal lock.
- Recovery must first prevent the old controller from issuing more commands, then verify that its remote pull, boot, switch and prune commands have exited or been terminated. A disconnected SSH session alone does not satisfy this check.
- Before clearing state, inspect Docker’s actual image/container state and the proxy route; terminating a Docker client does not prove the daemon operation was cancelled.
- `maintain` must refuse to run while old mutating work remains active or its ownership is uncertain. Only after that work is settled may recovery reconcile the recorded state and permit another attempt.
- Keep this an explicit fail-closed recovery procedure. Do not add automatic stale-lock takeover or a new distributed deployment scheduler.

Add a failure test that drops only the lock connection while a separate remote command continues executing. The next operator must remain blocked until the old work is conclusively settled.

### Rollback when the current application is broken

Separate rollback prerequisites from healthy-deployment prerequisites. In particular, do not reuse the current `rollback-begin` requirement that the active application pass `running()`.

- A stopped, unhealthy or missing current application container must not prevent rollback.
- Validate exclusive ownership, the recorded verified prior image and retained container, and its saved runtime configuration independently of the current application’s health.
- If required prior recovery material is missing or contradictory, fail with an actionable recovery report. Do not silently pull another image or choose an unrecorded version.
- Restore the retained version through Kamal, then verify actual identity, readiness and public responses before committing the new state.
- If the proxy itself is unavailable, report that separately and restore the pinned private proxy using the documented recovery procedure before attempting traffic switching.

A rejected candidate restores the previously verified active version. If cleanup fails after successful acceptance, leave the accepted version serving, report maintenance failure and block further deployments until it is resolved.

### Bounded storage

Keep one verified current application image and one distinct verified prior image. Temporary candidates and migration recovery images are allowed until acceptance. Protect Caddy, unrelated images and all required container references.

Measure incremental peak bytes and inodes on every distinct Docker/containerd backing filesystem. Account for compressed and extracted content; do not sum duplicate paths that share a filesystem. See [Docker’s containerd storage documentation](https://docs.docker.com/engine/storage/containerd/).

Use these explicit initial margins:

- Candidate headroom: 1.5 times the largest measured incremental peak, rounded upward.
- Filesystem reserve: the greater of 2 GiB or 10% of filesystem capacity.
- Inode reserve: the greater of 10,000 or twice the measured incremental inode peak.

Record the measurements and margins separately. Define and record the tested image-size envelope; larger candidates require renewed qualification. Insufficient capacity or unresolved recovery state must stop deployment before pulling. Never delete the only verified rollback to make room.

## 3. Persist the production topology and defer automation

### Permanent Caddy configuration

Replace the active legacy Compose definition with a **Caddy-only** definition during final handover:

- Remove the old application service and Caddy’s `depends_on: leapview-site` dependency from the active definition.
- Declare the Kamal network as an external network and attach Caddy to it declaratively. A one-time `docker network connect` is not the permanent configuration.
- Persist the `kamal-proxy:80` upstream in Caddy’s configuration. Preserve certificate/configuration volumes, published ports, HTTPS, compression and the `www` redirect.
- Keep a separate protected copy of the original Compose definition, environment and Caddy configuration for migration recovery; do not leave the old application in the routine Caddy maintenance definition.
- Keep application/proxy restart policies and the Kamal network persistent across Docker or host restart. Readiness checks must verify the actual proxy identity, network and Caddy route.
- Document Caddy maintenance using the Caddy-only Compose definition. Recreating Caddy must neither recreate the old application nor require an undocumented network-connect command.

Require Caddy recreation and host-restart tests in the disposable environment. On the production host, verify Caddy recreation and perform the controlled restart/recovery acceptance check before deleting migration recovery resources. Report any interruption rather than promising zero downtime.

### Workflow changes

Narrow `.github/workflows/site-deploy.yml` to build and qualify images for this phase:

- Remove legacy `production` tag promotion and activation polling.
- Remove the unqualified Kamal activation/access jobs and proposed CI SSH action from this PR’s active implementation.
- Preserve normal PR CI/security checks and the existing production image qualification process.
- Update the operator entrypoint to use the manual commands.
- Keep the legacy updater disabled and guard installed legacy entrypoints against restarting it after handover.

Automatic image publication remains available. Automatic VPS activation is a separate follow-up requiring its own access and qualification work.

## 4. Qualification and PR handling

Run the complete revised operator implementation against a disposable Docker/containerd environment matching production. Use test-only transport/runtime bindings; expose no production admission bypass.

| Scenario | Acceptance requirement |
| --- | --- |
| Two distinct admitted images | Exact platform, runtime and version-specific release metadata verified. |
| Ten update cycles | Settled storage remains bounded, retaining current plus distinct prior. |
| Unhealthy candidate or failed public acceptance | Working version survives or is restored; failed attempt is safely recoverable. |
| Stopped, missing and unhealthy current container | Recorded prior can be restored without requiring current health. |
| Offline rollback from a fresh operator session | Uses retained image/container and saved runtime record; no registry access. |
| Corrupt or missing recovery material | Fails without guessing, destructive cleanup or an unverified image pull. |
| Same-image deployment | Verified no-op without restart or pruning. |
| Interrupted pull/switch or lost acceptance response | Actual state is inspected; unresolved work blocks subsequent mutations. |
| Lock connection lost while remote work continues | New operator and maintenance remain blocked until previous work is settled. |
| Competing operators | Second attempt performs no pull or mutation. |
| Cleanup failure after acceptance | New version remains live; explicit maintenance recovery succeeds. |
| Insufficient bytes/inodes | Rejects before pulling and preserves recovery material. |
| Foreign aliases, shared layers and digest references in `RepoTags` | Unrelated resources survive; valid owned references are accepted. |
| Caddy recreation and host restart | HTTPS returns through the declared Kamal network; no legacy application is recreated. |
| Migration restoration to Compose | Original routing and image can be restored from the protected recovery configuration. |

Run focused tests, `task ci`, workflow linting and required hosted checks on the final commits. Resolve the local test prerequisite in an isolated environment rather than repairing the shared Docker daemon as unrelated work. Skipped checks do not satisfy acceptance.

### PR sequence

1. Reconcile #752 with current main, implement the manual solution, complete qualification and obtain review.
2. Update #751’s stale disk/production status and record the real VPS results. Retain reproducible evidence, run normal checks and obtain review.
3. Drain old production workflow runs, then merge #752 with automatic activation absent. Wait for its production image build and admission to succeed before selecting image A.
4. Merge reviewed #751 afterward and qualify a second distinct production image B. If an intervening main change supersedes either build, select eligible successfully qualified main revisions instead; never deploy an incomplete build merely to preserve this merge sequence.
5. Keep #748 uninstalled as the fallback until permanent acceptance, then close it as superseded with links to the replacement and evidence.

Both selected images must include the required service label and pass production admission. Trial-package images do not qualify for permanent deployment.

## 5. Controlled migration and completion

1. Refresh host inventory, website health, disk state and exact current/prior image identities. Save protected legacy recovery configuration and commands.
2. Confirm the old updater is disabled, previous deployment work has settled and exclusive ownership is established.
3. Bootstrap the pinned private Kamal proxy and production image A while retaining the original Compose application for recovery. Verify A privately.
4. Install the persistent Caddy-only topology, switch its upstream with a timed restoration safeguard, and verify public health/readiness, container identity, build metadata, docs, release metadata, download links, assets and `www` behavior.
5. Demonstrate restoration to the protected legacy Compose configuration and return to Kamal A. Write permanent handover state only after successful verification.
6. Deploy B through the completed operator command. Roll back locally to A, verify it, then restore B. Also demonstrate rollback with the current application stopped in the qualified test environment.
7. Verify Caddy recreation and controlled host restart against the permanent configuration. Confirm the old application is not recreated and HTTPS recovers through Kamal.
8. Verify pruning leaves B plus distinct verified A. Remove migration-only application containers/image references after recovery acceptance. Retain Caddy’s data and protected textual recovery/audit records.
9. Record final digests, runtime versions, capacity margins, retained images, operator commands and interrupted-operation recovery instructions. Leave automatic deployment disabled.
10. Observe website health and disk usage for 24 hours before closing #748. Any unresolved deployment or maintenance failure postpones closure.

### Definition of done

A production-qualified site runs permanently behind Caddy/Kamal. The actual operator deployment, broken-current rollback and interrupted-operation recovery paths pass. Caddy recreation and host restart preserve the topology. Storage remains bounded, the retired updater cannot interfere, required checks/reviews are complete, and the runbook matches the live host.

**Deferred:** GitHub runner access and automatic deployment activation. Do not report manual migration completion as completed CI/CD.

### Review corrections incorporated

1. Persistent Caddy-only Compose configuration, declarative external networking, and recreation/restart acceptance tests.
2. Rollback prerequisites independent of the current application’s health, with stopped/missing/unhealthy-current tests.
3. Explicit handling of remote work that outlives a lost lock connection; no automatic takeover or state clearing while ownership is uncertain.
