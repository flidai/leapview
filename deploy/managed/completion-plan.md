# PR #760 takeover and completion plan

Reviewed on 2026-09-30. Owner: Ganesh K. Implementation: PR #760.

The deployment proposal from [PR #744](https://github.com/flidai/leapview/pull/744)
has been removed from main pending renewed review. The baseline and overlap gate
below describe the earlier roadmap, not permission to deploy overlapping processes
against the locked application home. The separately merged
[maintenance implementation](maintenance.md) remains available; architecture
acceptance and full managed-profile qualification were outstanding at that review.

**9 October 2026 amendment:** the project owner has now accepted the bounded
maintenance application update design. The [decision record](decision-reconciliation-20261009.md)
supersedes the historical overlap requirement and pending update-design statements
below. The old proposal remains reference-only; profile qualification and
operational acceptance still require their own evidence. This document preserves
the earlier takeover findings and does not describe the current issue completion
state; use the project's delivery ledger for current PRs and results.

## Execution baseline

- Original scaffold: `25d3ee01e2aa46b49ec0d2c79b49330db12ffe37`.
- Current main incorporated: `ac1433754574d9f210c2705f3ba207aabbc097cf`.
- Reference decision: proposed ADR-0025 in PR #744 at
  `5938aeaa8a02c2b84084036961d7ce7edc29ddd9` (historical numbering; later ADR-0028).
- The user's revised Nix migration roadmap dated 2026-09-30 is the broader
  sequencing reference. [Repository roadmap](../../plan.md) contains the earlier
  version. The revised roadmap additionally calls out protected first-reviewer
  issuance before first publication.
- PRs #767, #752 and #751 are merged. Nix development candidates and stateless
  site qualification do not qualify this managed stateful application profile.
- Locked host inputs remain unchanged; see [flake.lock](nixos/flake.lock).

## Review findings and this continuation

The infrastructure scaffold has sensible public/private boundaries, protected
servers, role separation, runtime secret-file references and bounded backup
schedules. The existing rehearsals are explicitly component evidence. Keeping
their limits visible is part of the delivery contract.

1. **Container forwarding was unfiltered.** Host INPUT rules do not filter
   Docker's DNAT/FORWARD path. The original host accepted a published bypass port
   in an isolated kernel reproduction. The app module now installs an IPv4/IPv6
   forwarding policy before Docker, permits only public original ports 80/443,
   rejects private/bypass ingress and preserves outbound responses. Atomic policy
   replacement avoids flushing Docker's rules. Userland proxies are disabled.
2. **Routine CI did not build the hosts.** The host job previously evaluated the
   flake and built assertion output only. It now builds both host closures and
   deploy-rs checks, checks formatting and executes the kernel regression plus
   the isolated real-Docker test using the host's locked Docker package.
   Manual runs can additionally select the real-Docker reboot fixture.
3. **The README prescribed stop-first replacement.** That historical workaround
   conflicts with Stage 4's required normal Kamal sequence. The README now records
   the overlap ownership gate and keeps stop-first results as historical evidence.
4. **First publication has a bounded installed-host slice, not full Stage 4
   acceptance.** The existing authoring path nominates a distinct reviewer
   before planning and uses a separate reviewer credential for approval. The
   installed-host path now records a private-bootstrap phase before starting
   persistent services. The app and internal-CA Caddy proxy stay loopback-bound
   while publication is pending. `activate-first-install` requires direct
   `/readyz` 200, applies the public Caddy configuration, rechecks readiness,
   and only then records the public phase; a failed activation restores private
   configuration. Compose and marker replacement are sequential: an abrupt
   interruption after public Caddy activation but before the public marker is
   durable can leave Docker restarting the public configuration until a pending
   `start` reconciles it. This window follows direct `/readyz` success. The
   protected fresh-host qualification is scoped to exercise
   the authoring path against its installed external-PostgreSQL target, check
   committed candidate bindings, and observe `/readyz` 503→200.
   The new activation path still needs a rebuilt candidate and hosted guest run
   before it counts as acceptance evidence. This is a bounded first-publication
   slice, not full Stage 4 acceptance. With external HTTPS, the app remains
   loopback-bound and the operator-owned proxy must withhold its route until
   activation. A normal overlapping candidate cannot become ready on the shared
   home: `serve` takes the exclusive home lock before app build, and `/readyz`
   requires an active runtime lease. The owner-selected bounded-maintenance
   direction remains pending ADR review and qualification; it is not an accepted
   lifecycle contract.
5. **The native fixture did not verify mount isolation.** Its invocation supplied
   a mount namespace, but its guard checked only PID 1 and an empty network. The
   reviewed fixture now creates its own namespaces and checks mount/network/PID
   identities against the parent before mutation. Four safety tests cover shared
   namespaces, privileges and entrypoint refusal before system commands. The
   network regressions additionally cover direct container routes and real Docker
   IPv6, not only translated IPv4 host ports.
   Root-owned daemon state now uses a temporary directory outside the checkout;
   the previous `.tmp` location made concurrent contract generation fail while
   recursively walking the repository.
6. **A port allowlist does not make metrics private.** `/metrics` shares the
   application listener. The current public proxy routes that path with bearer
   authentication; the network fixtures do not enforce path restrictions. Private
   collection/exposure and external denial remain part of monitoring qualification.
7. **SSH CIDR validation accepted default-route aliases.** The host module rejected
   `/0` but accepted `/00` and malformed addresses, allowing unexpectedly broad
   rules or firewall startup failures. It now requires explicit positive,
   bounded masks and valid unambiguous addresses before generating rules. Eleven
   invalid-input cases and a valid IPv6 operator case cover the boundary; both
   role builds and deploy-rs contracts pass with the stricter validation.

The forwarding test uses generated, disposable data and isolated network/mount
namespaces. It creates no provider resources and changes no existing host
firewall. The guest fixture uses real Docker with a locally built probe image;
it does not download an application or exercise application release compatibility.

## Remaining slices in dependency order

| Roadmap gate | Next implementation and acceptance evidence |
| --- | --- |
| Stage 1: Linux tool adoption (separate track) | Merged Nix candidates exist; prove fresh-environment contracts with the locked tools as defaults, migrate callers, preserve generation/browser ordering and supported non-Linux paths before removing duplicate installers. This PR does not complete that adoption. |
| Stage 2: release adoption (separate track) | Unchanged supported-host compatibility matrix, artifact/architecture qualification, exact-digest protected security admission including native dependencies, inventory/provenance and immutable promotion. Candidate availability does not establish production admission. |
| Stage 3: host updates/recovery | Private reviewed inventory and state ownership; disposable fresh install; deploy-rs failed activation/connectivity recovery; retained known-good boot generations; rescue access; separate host-role reboot; mount/secret ownership; external port checks; disk pressure, GC and cache-outage exercises. Updates must never execute installation formatting. |
| Stage 4: first publication | The protected external-PostgreSQL guest qualification is scoped to exercise private first-user bootstrap → pre-planning reviewer nomination → distinct approval → exact committed candidate → loopback `/readyz` 503→200. Product code now withholds managed Caddy's public configuration until explicit readiness-gated activation; a rebuilt candidate and hosted guest run must verify that path. External HTTPS remains operator-gated. Complete managed acceptance still needs exact target/project/environment and policy binding; publisher self-approval, foreign scope, expired authority, permission widening, concurrent activation and failed-issuance rollback cases; and public traffic qualification without development credentials or SQL bypasses. |
| Stage 4: shared lifecycle | Extract reusable lifecycle admission from Compose without adding a second container owner. Enforce compatible release pairs, preflight/migration boundaries, serialized mutations and exact approved artifact identity. Resolve shared-home ownership and worker/publication fencing before ordinary Kamal overlap. Exercise candidate failure, interrupted switch, drain, SSE/uploads/jobs and rollback after writes with retained configuration/secrets and offline images. |
| Credential prerequisite | Review the focused credential-lifecycle ADR before implementing customer credential formats, activation or rotation. Host provisioning remains independent. |
| Stage 5: complete managed qualification | Two real hosts; protected keys; pgBackRest PITR and file-consistent recovery; replacement/fencing of each host; fresh-pool analytical rebuild; private metrics collection/exposure and actionable off-host monitoring; operator revocation; measured downtime, recovery, write-loss and manual work. Record exact images, configuration and host generations. |
| Stage 6: adoption/retirement | Internal handover, scoped customer eligibility, observation criteria and recovery acceptance. Retire each old script only after its callers and recovery responsibilities migrate. |

This PR remains a scaffold. It does not adopt Nix-built release images, qualify
production application deployment, change the demo's Compose owner, provision
customers or retire recovery scripts. Stage 2's compatibility and exact-artifact
security admission remain independent prerequisites for Nix release adoption.

## Validation record

See [takeover validation](rehearsal-takeover-2026-09-30.md) for the tested source,
input identities, results and limitations. Earlier evidence remains in the two
2026-09-28 rehearsal records; it must not be attributed to this changed host
configuration.
