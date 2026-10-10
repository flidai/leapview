# PostgreSQL major maintenance rehearsal

PostgreSQL major maintenance is separate from application image handoff and
NixOS generation rollback. The current LeapView control schema requires
PostgreSQL 18 (`uuidv7()` appears in migration 001). No supported LeapView 17
installation or PostgreSQL 19 deployment is implied by this rehearsal.

Run the locked, disposable database-component check on an AMD64 Nix builder:

```sh
nix build --no-update-lock-file \
  .#checks.x86_64-linux.postgres-major-maintenance --no-link --print-out-paths
```

Read `report.json` under the printed output. The normal Nix development workflow
also runs this check through `nix flake check`. The check creates its own private
directories and Unix sockets as an unprivileged build user. It accepts no existing
database directory, connection URL or production credential and opens no TCP port.

The locked PostgreSQL 17 tool creates representative acknowledged records,
JSON/TOAST values, binary data, an identity sequence and separate owner/runtime
roles. `pg_basebackup` creates a physical backup, and `pg_verifybackup` verifies
it before the restored cluster is started. The restored system identity must
match the original. PostgreSQL 18 then runs `pg_upgrade --check` and
`pg_upgrade --copy` on that stopped restore. Readback checks the exact data
fingerprint, runtime permissions and sequence; a newly acknowledged write must
survive a candidate restart. The copied old cluster is also restarted and checked.

This follows the [PostgreSQL 18 upgrade procedure](https://www.postgresql.org/docs/18/pgupgrade.html).
Copy mode preserves the old files; link/swap mode is deliberately excluded.
Both clusters have checksums enabled. The rehearsal never uses `--no-sync`.

The safe old-cluster rollback point ends **before the candidate accepts any
writes**. The test deliberately confirms that the retained old cluster still has
100 records after the upgraded candidate acknowledges record 101. Reopening the
old cluster at that point would lose acknowledged data. A NixOS rollback or an
old application image cannot repair that divergence.

## Applying the procedure to a supported future transition

Before scheduling customer maintenance, qualify the exact supported old/new
PostgreSQL binaries, extensions, source schema and target LeapView artifacts on
restored installation data. The current component fixture does not establish
that compatibility. Unsupported major versions remain ineligible.

Use the installation's accepted recovery set and protected pgBackRest/WAL plus
file recovery procedure. Verify required key material, retention, primary fencing
and independent controller state. Keep public writes and background mutation
closed, record the acknowledged frontier, and stop both PostgreSQL clusters before
the checked copy-mode upgrade. Preserve the old data, old binary closure,
configuration and matching secrets until the accepted retention boundary.

Before reopening service, verify full product schema/role ownership, source
publication and DuckLake catalog/snapshot identity, governed queries, credentials,
jobs and file/acknowledgement reconciliation. Test recovery on the selected host
profile and record interruption, restore time and capacity. Customer eligibility
and recovery bounds require those results and owner acceptance. The local
`checkAndUpgradeSeconds` measures only disposable check/upgrade execution.

If qualification fails before writes reopen, stop the candidate and verify the
retained old cluster before using it. After new writes, keep service closed and
follow the accepted restore/reconciliation procedure; never silently return to
the pre-upgrade directory. Do not combine database-major maintenance with a normal
application release or delete either recovery path merely because this check passed.
