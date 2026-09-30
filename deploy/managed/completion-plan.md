# PR #760 takeover and completion plan

Reviewed on 2026-09-30. Owner: Ganesh K. Implementation: PR #760.

## Execution baseline

- Original scaffold: `25d3ee01e2aa46b49ec0d2c79b49330db12ffe37`.
- Current main incorporated: `ac1433754574d9f210c2705f3ba207aabbc097cf`.
- Reference decision: proposed ADR-0025 in PR #744 at
  `5938aeaa8a02c2b84084036961d7ce7edc29ddd9`; it remains open for review.
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
   deploy-rs checks, checks formatting and executes the kernel regression.
   Manual runs can additionally select the real-Docker reboot fixture.
3. **The README prescribed stop-first replacement.** That historical workaround
   conflicts with Stage 4's required normal Kamal sequence. The README now records
   the overlap ownership gate and keeps stop-first results as historical evidence.
4. **First-reviewer token issuance remains blocked.** Current main already has
   independent reviewer nomination under the unpublished-target lock. Personal
   project-token issuance still obtains project permissions from an active
   serving snapshot, which does not exist at first publication. Nomination alone
   does not supply a pre-activation credential. Widening the general resolver to
   current database policy would change runtime authority and is not a safe fix.

The forwarding test uses generated, disposable data and isolated network/mount
namespaces. It creates no provider resources and changes no existing host
firewall. The guest fixture uses real Docker with a locally built probe image;
it does not download an application or exercise application release compatibility.

## Remaining slices in dependency order

| Roadmap gate | Next implementation and acceptance evidence |
| --- | --- |
| Stage 3: host updates/recovery | Private reviewed inventory and state ownership; disposable fresh install; deploy-rs failed activation/connectivity recovery; retained known-good boot generations; rescue access; separate host-role reboot; mount/secret ownership; external port checks; disk pressure, GC and cache-outage exercises. Updates must never execute installation formatting. |
| Stage 4: first publication | Extend the fenced, audited bootstrap contract to issue a bounded independent reviewer credential before activation. Bind exact target/project/environment and policy revision. Nominate before planning. Test publisher self-approval, foreign scope, expired authority, widening, concurrent activation and rollback of failed issuance. Qualify roles/schema/pool → private bootstrap → publisher/reviewer credentials → plan/approve/publish → `/readyz` → public traffic without development credentials or SQL bypasses. |
| Stage 4: shared lifecycle | Extract reusable lifecycle admission from Compose without adding a second container owner. Enforce compatible release pairs, preflight/migration boundaries, serialized mutations and exact approved artifact identity. Resolve shared-home ownership and worker/publication fencing before ordinary Kamal overlap. Exercise candidate failure, interrupted switch, drain, SSE/uploads/jobs and rollback after writes with retained configuration/secrets and offline images. |
| Credential prerequisite | Review the focused credential-lifecycle ADR before implementing customer credential formats, activation or rotation. Host provisioning remains independent. |
| Stage 5: complete managed qualification | Two real hosts; protected keys; pgBackRest PITR and file-consistent recovery; replacement/fencing of each host; fresh-pool analytical rebuild; actionable off-host monitoring; operator revocation; measured downtime, recovery, write-loss and manual work. Record exact images, configuration and host generations. |
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
