# Managed scaffold: remote component rehearsal, 2026-09-28

Status: **component tests passed; production qualification remains incomplete**.
Scaffold revision tested: `e7f7ec312db1e820e2730cc9285da10ffb152da2`.

## Environment and boundaries

Used an existing Debian 13 x86_64 VPS with 31 GiB RAM and no KVM device.
Installed Debian's Docker engine/CLI and Compose packages. The host was not
reimaged, rebooted or converted to NixOS. SSH and Tailscale remained available.

All database/file contents and credentials were generated fixtures. PostgreSQL
and the S3 fixture had no published host ports. The successful proxy tests used
an internal Docker network without published ports. No customer data, Hetzner API
credentials, real S3 account or public DNS/certificate issuance was involved.

## Results

| Exercise | Evidence | Result |
|---|---|---|
| Complete NixOS build | Locked `nix flake check path:/work --no-update-lock-file`, including both host system closures, host contracts, deploy-rs activation checks and deployment schema | Passed |
| PostgreSQL client security | SCRAM connection with `sslmode=verify-full`; plaintext and an untrusted CA both rejected | Passed |
| pgBackRest full backup | AES-256-CBC repository encryption, certificate-verified S3 transport, successful `stanza-create`, `check` and full backup | Passed |
| Differential backup | Additional committed row, then `--type=diff backup` | Passed |
| PostgreSQL recovery | Fresh volume, latest backup plus WAL, named recovery target, promotion and exact row assertions | Passed |
| Restic | S3 backup, `check --read-data`, fresh-directory restore and SHA-256 comparison after overwriting the source file | Passed |
| Proxy streaming | Three SSE events arrived at approximately 0, 1 and 2 seconds with response buffering disabled | Passed |
| Failed release | `/readyz` returned 503; deployment failed while the previous target remained reachable | Passed |
| Healthy release | New ready target received traffic | Passed |
| Restart-based rollback | Stopped the previous upstream container, restarted it and switched traffic back | Passed |
| Proxy restart | Restarted the same proxy container; its routing state remained available | Passed |

### Recovery sequence

1. Create a probe table and row 1; take a full backup.
2. Commit row 2; take a differential backup.
3. Commit row 3 (present only in subsequent WAL).
4. Create the named restore point `before_destructive_write`.
5. Truncate the table and commit row 999; verify the destructive write occurred.
6. Switch WAL and run the pgBackRest archive check; stop the primary.
7. Restore into a new volume with
   `pgbackrest --stanza=default --type=name --target=before_destructive_write --target-action=promote restore`.
8. Start the recovered database; verify it is promoted and contains exactly rows
   1, 2 and 3, with no row 999.

Restore plus readiness took 4.8 seconds for this tiny fixture. Proxy restart-based
rollback took about 0.4 seconds for a synthetic Python HTTP server. **Neither is a
LeapView downtime estimate, production recovery objective or performance benchmark.**

## Versions

- Nix runner: `nixos/nix:2.31.2`, digest
  `sha256:29fc5fe207f159ceb0143c25c19c774062fee02ce5eda118f3067547b3054894`.
- NixOS inputs: the checked-in [flake.lock](nixos/flake.lock), including nixpkgs
  `cf5e76507c6e23b59f7e0ffcc7baa2a39ddd8442`.
- Container recovery fixture: PostgreSQL 18.6, pgBackRest 2.58.0, Restic 0.18.1.
  These were container packages, not the services booted from the NixOS closures.
- S3 fixture: the repository's source-built MinIO test image at source revision
  `07c3a429bfed433e49018cb0f78a52145d4bedeb`.
- kamal-proxy: v0.9.2, digest
  `sha256:826a6f66c6ba26ac26197ac8755804403c9bb617b90cfac25c7972154c5328ab`.

## Findings and limits

- The proxy's `max-request-body` option applies when request buffering is enabled.
  The scaffold disables request buffering, so this option is **not an enforced
  upload cap**. Application/protocol upload limits require separate qualification.
- Docker's initial PostgreSQL entrypoint starts a temporary database. Enabling
  archiving before creating the backup stanza complicated fixture startup. The
  successful fixture initialized its cluster separately before the archive-enabled
  run. Production bootstrap must qualify its own stanza/archiving startup order.
- The HTTP upstream was a synthetic server. These tests exercised **kamal-proxy**,
  not the Kamal release controller or LeapView's application/migration lifecycle.
- Proxy TLS/ACME renewal, uploads, real `/updates` sessions and draining across
  cutover were not tested. Restarting the same proxy container does not prove
  replacement-container or lost-host recovery.
- No NixOS VM boot, disko installation, deploy-rs activation on a host, host reboot,
  private-network firewall exercise or Hetzner provisioning was performed.
- The S3 repository was on the same rehearsal host. This exercises backup protocol
  and restore correctness, not off-host durability or provider-loss recovery.
- PostgreSQL recovery does not prove DuckLake catalog/file consistency, source
  republishing, runtime role isolation or independently recoverable keys.

All test containers, test networks, database/object volumes and generated fixture
credentials were removed. Docker, cached test images, the Nix build cache and
rehearsal scripts/logs remain on the test host for subsequent work. No application
service was left deployed.

Next: implement the canonical bootstrap/release adapter, then perform an actual
LeapView deployment and recovery rehearsal; qualify the NixOS installation on a
separate disposable host.
