# Managed scaffold: application and guest-boot follow-up, 2026-09-28

Status: **partial integration evidence; not production qualification**.
This follows the [component recovery rehearsal](rehearsal-2026-09-28.md).
Scaffold code revision: `5f38b145e` (plus this evidence/documentation update).

## Environment

Used the same Debian 13 test VPS over ordinary SSH on Tailscale, with the operator's
pinned ED25519 fingerprint. No host reimage, disk formatting or host reboot occurred.
The SSH operator was temporarily added to the Docker group for native Kamal access.
All application identities, passwords, certificates and data were disposable fixtures.

PostgreSQL ran in an isolated Docker network without a published port. A disposable
registry and kamal-proxy published only loopback ports (15000, 18080, 18443), reached
through SSH forwarding. HTTPS was checked with the fixture CA and hostname validation.
Browser tests accepted the fixture certificate; separate curl checks verified trust.
No public DNS, ACME, real S3 account or Hetzner provisioning was involved.

## Real application procedure

1. Initialize disposable PostgreSQL roles using the repository's test initializer,
   replacing every default password with generated credentials. This is **not** a
   production role provisioning implementation.
2. Run canonical `admin delivery pool qualify`, dry-run/bootstrap admission,
   `admin initialize`, pool bootstrap `--apply` and credential acknowledgement with
   separate operation credentials. Mount the database CA and use verified TLS.
3. Deploy the predecessor through the pinned Kamal 2.12.0 controller and
   kamal-proxy v0.9.2. Bootstrap is private and temporarily probes `/healthz`.
4. Claim the project through `bootstrap-project`, generate the built-in sample
   with `init`, upload its data using `data sync`, review the retained plan and
   resume `deploy` with its exact confirmation digest. Production policy correctly
   leaves the publication pending independent approval.
5. Log in through the browser, change the initial password, create an independent
   reviewer and nominate the `release_approver` role through the normal admin
   command. Reviewer login/password change succeeds.
6. Record managed-data checksums. Explicitly stop the old application through
   Kamal before deploying the candidate, preserving the single-owner home lock.
7. Verify the running revision, CA-verified HTTPS liveness, authentication using
   the already-rotated password, profile identity, real `/updates` SSE response,
   and unchanged uploaded-file checksums.
8. Stop the candidate and invoke native `kamal rollback previous`; repeat the
   version, HTTPS, authentication, SSE and file checks.

The tested source revisions were:

| Image | Source revision | Original registry manifest digest |
|---|---|---|
| Previous | `59cbc0d18124c0090443d32ff4af7792246188b9` | `sha256:b68c127b934bc984d0a8c1d4066337f4954d6f476cc6f16fa23dc26311940702` |
| Candidate | `bbdaa69edab52136a56c8abebf57b97904081bc4` | `sha256:5243583fb4fdc363d2d776e348a1e345f035076dcd869ba1c3ae0a2265a489df` |

These are development artifacts from main, not qualified production releases.
Both needed metadata-only wrapper images adding `service=leapview` for Kamal;
the executable and application layers were unchanged. Wrapper digests differ from
the original manifests above. Future release images get the label in the canonical
Dockerfile. This rehearsal does not replace approved-digest verification.

## Findings fixed in the scaffold

- Add the required Kamal service label to the release Dockerfile, covered by the
  existing release-identity contract test.
- Supply `LEAPVIEW_AGENT_CREDENTIAL_KEY`, plus separate bounded control and DuckLake
  maintenance URLs needed by the serving process's retention services. Keep schema
  owner/migrator, upgrade-coordinator, backup and provider credentials out of it.
- Supply the configured public hostname to proxy health checks. The application
  correctly rejects an internal container hostname. The small `probe_host.rb`
  adapter exposes kamal-proxy's native option missing from Kamal 2.12's schema;
  native command-construction tests cover it. Allowed-host validation stays enabled.
- Document that `/readyz` requires an active deployment, while `/healthz` supports
  a private onboarding phase only. Keep the production template on `/readyz`.
- Document drain/stop/start ordering for the shared home. Ordinary overlapping
  Kamal replacement is unsuitable for the current single-owner layout.

## Application blocker and limits

First protected publication remains blocked: the nominated independent reviewer
can authenticate, but creating a narrowly scoped `delivery.approve` API token from
`/admin/personal-settings/command` fails with `400 no active LeapView serving state`.
The personal token service asks for effective permission options before a serving
generation exists. This was reproduced after password rotation with the canonical
permission profile and exact project scope. The preceding malformed-scope request
was correctly rejected separately. No database edits or approval-policy bypasses
were used to manufacture an active generation.

The candidate was built and sealed and its publication remained pending approval.
Consequently `/readyz` remained 503 (`no_active_deployments`), and image replacement
and rollback used **liveness**, not production serving readiness. Their persistence
checks cover initialized control state and uploaded data, not active dashboard
queries, active pipeline work or rollback after schema-changing writes. The real
SSE check covers profile signals; the earlier synthetic test separately exercised
event timing and buffering. Full dashboard cutover/draining remains unqualified.

The next application slice must expose an authorized, independently scoped first
reviewer credential path before initial activation, with denial tests for the
publisher, wrong project, expired credentials and widened permissions. Nominate
the reviewer before creating a fresh plan so that the plan binds the final policy
revision. Then repeat this rehearsal with `/readyz`, a published dashboard,
in-flight work and migrations.

## NixOS guest test

The optional `task managed:hosts:boot-test` imports the actual app/database modules
into three isolated QEMU guests, with a third host representing another private
network client. It is exposed as a flake package rather than a default CI check.
The final build exited successfully and all assertions passed.

The test supplies TLS files at runtime, verifies an app-to-database connection with
`sslmode=verify-full`, rejects plaintext and a connection from the third host, then
reboots the database and application guests. The database's probe role must persist;
PostgreSQL, Docker and SSH services must return.

The fixture uses static test-network addresses, disables GRUB installation and
starts containerd separately to tolerate software emulation. The initial attempt
hit dockerd's internal containerd startup deadline; the next attempt needed the
upstream test driver's explicit `allow_reboot=True`. Both are fixture corrections,
not changes to the production host modules. Real TLS keys never enter the flake.

This is a guest service test, not Disko installation, firmware boot, Hetzner DHCP,
Tailscale enrollment, operator key authentication, real backup credentials or
host-loss recovery qualification. The emulated network-online unit timed out
before Docker started; network-online convergence still needs qualification on
the real interface/DHCP configuration. Backup and Tailscale logs also contain
expected errors because their external credentials/enrollment are absent. The
assertions establish specific service behavior, not an all-units-healthy host.
PostgreSQL/Restic backup recovery was tested separately in the earlier container
rehearsal.

## Validation and cleanup

- Full `task ci` passed after merging main at `bbdaa69ed`.
- Native Kamal configuration tests passed: 4 tests, 23 assertions.
- The focused release-image identity contract passed.
- Nix formatting and final flake evaluation passed against the locked inputs.
- The three-guest NixOS boot/reboot test passed under software emulation. It
  checked verified TLS, plaintext rejection, third-host rejection, persisted
  PostgreSQL state, and Docker/SSH after reboot. The test runner then removed
  its guest machines and container. See the network-online limitation above.
- Native Kamal candidate deployment and rollback both completed; the version,
  HTTPS, login, real profile SSE and managed-data checksum checks passed after each.

Removed the application/proxy/database/registry containers, their data volumes,
the test network, runtime secrets, browser sessions and SSH forwarding. Removed
the temporary Docker group membership and registry login. SSH and Tailscale remained
active, and test application ports were no longer listening. Docker packages,
image/bundle/Nix caches and rehearsal scripts/logs remain. No LeapView instance is
left deployed. These tests establish neither an SLA nor off-host recovery.
