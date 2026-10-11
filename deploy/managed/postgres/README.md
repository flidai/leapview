# Native managed PostgreSQL bootstrap

This small operator command provisions the fixed production role/database
boundaries on one **fresh, disposable PostgreSQL 18 qualification cluster**. It
uses native PostgreSQL peer authority, not the development initializer or its
default passwords. It is not a database reconciler, migration service or backup
engine. Existing customer clusters and previously provisioned clusters are
ineligible, including an exact second invocation.

Privately create a directory owned by the operating-system `postgres` user with
mode `0700`. Supply these seven distinct, randomly generated password files,
owned by that user with mode `0600` or `0400`:

- `control-runtime-password`
- `control-migrator-password`
- `control-maintenance-password`
- `control-upgrade-coordinator-password`
- `ducklake-runtime-password`
- `ducklake-migrator-password`
- `ducklake-maintenance-password`

Each value must be 32–128 ASCII letters, digits, `_` or `-`, optionally followed
by one newline. There are no defaults. Use at least 32 random bytes encoded as
base64url. Linked, shared, public, malformed and duplicate credentials fail
before role creation. Keep the directory outside every Nix flake/source tree and
public evidence. Deliver it through the protected operator channel; it is not a
Terraform input or cloud-init payload.

After independently recording the fresh server's actual
`pg_control_system().system_identifier`, run as the installed `postgres` user:

```sh
sudo -u postgres /absolute/locked/python3 deploy/managed/postgres/bootstrap.py \
  --psql /nix/store/REVIEWED-postgresql-18/bin/psql \
  --socket /run/postgresql --port 5432 \
  --credentials /var/lib/leapview-postgres-bootstrap \
  --system-identifier ACTUAL_ENROLLED_SYSTEM_IDENTIFIER
```

Use the actual pinned executable path from the selected installed module. The
command authenticates over the explicit Unix socket as PostgreSQL `postgres`,
requires peer authentication, the enrolled cluster identity and PostgreSQL 18,
and refuses existing non-system roles, extra databases or application objects.
Ambient libpq configuration and psql startup files are ignored. Plaintext
passwords stay out of SQL, argv, environment and output; SQL receives salted
SCRAM verifiers over stdin with statement logging disabled. It changes neither
the host's HBA rules nor TLS, listener, archiving or NixOS configuration.

Role creation uses one native transaction. PostgreSQL database creation is a
separate operation; a failure or interruption after role commit can leave an
incomplete setup. **Do not rerun or reconcile it.** Stop and inspect the retained
disposable cluster, then explicitly recreate that qualification cluster and its
credentials before restarting the fresh drill. This procedure is never a reason
to destroy an existing installation. Output is successful only after both
databases and their grants are established.

The owner/runtime/migrator/maintenance boundaries mirror the canonical production
[`bundled-init.sh`](../../compose/postgres/bundled-init.sh). Owners are non-login;
only migrators may assume an owner. Runtime, maintenance and the control upgrade
coordinator have no owner membership, superuser, role/database creation or RLS
bypass. The operation-only upgrade coordinator is present before canonical
control migrations, which install its bounded operation grants. Readonly and
backup capability roles remain non-login.

This command does not apply application schemas or admit a physical pool. Follow
the existing installer sequence: qualify the external physical pool with the
canonical application commands, deliver reviewed operator input, run
`leapviewctl host install`, then complete independent first-publication approval.
Control migrations and DuckLake bootstrap use the distinct operation credentials;
ordinary serving receives only runtime and bounded maintenance credentials.
The database module continues to require verified TLS/SCRAM from the app host.

Retain the explicit credential/key recovery copies for the actual cloud drill.
Passing these local tests establishes role provisioning and refusal boundaries;
it does not establish cloud installation, TLS reachability, backup restore,
application publication or full managed qualification.

Run the native regressions with the selected locked PostgreSQL package:

```sh
POSTGRES_BIN=/nix/store/REVIEWED-postgresql-18/bin \
  /absolute/locked/python3 -m unittest discover \
  -s deploy/managed/postgres -p '*_test.py'
```

The tests initialize disposable clusters as the current unprivileged user, map
that user to PostgreSQL `postgres` for actual peer authentication, and authenticate
runtime roles using SCRAM. They verify migration/owner separation, cross-database
denial, bounded maintenance writes, repeat/foreign-state refusal and private input
failures. No cloud resources or existing database are accessed.
