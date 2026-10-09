# Managed original-writer fencing and recovery qualification

Managed replacement recovery must establish that the original PostgreSQL writer
cannot restart before restoring or admitting a replacement. An expired controller
lease, a successful backup, an SSH timeout, and an unreachable database are not
physical fencing evidence.

The database NixOS module installs `leapview-postgres-fence`. It binds its durable
marker to the target, exact RecoverySet ID/frontier digest, enrolled machine ID,
logical cluster identity, and PostgreSQL system identifier. It checks the actual
host and database identity, persists/fsyncs the private marker, then stops
PostgreSQL. Successful verification also requires clean shutdown, inactive
systemd state, and absence of the PostgreSQL PID file. The startup condition
prevents systemd from restarting that writer after controller interruption or
host reboot.

The helper checks the current system, every retained system-profile generation,
and their boot specialisations
for the same startup condition. An old unguarded generation is a hard failure;
the helper never deletes old generations or rewrites boot configuration. Install
and qualify guarded generations, then resolve retention explicitly before using
this recovery path. Privileged manual PostgreSQL startup or editing the boot
configuration remains outside this fence's trust boundary. Recovery must fail
closed if the original host cannot be observed; provider-level fencing for an
unreachable host requires a separately qualified adapter.

## Operator entrypoint

`leapviewctl host fence-recovery` reads the exact RecoverySet from the existing
PostgreSQL recovery authority. Its enrollment input is private JSON:

```json
{
  "targetID": "enrolled-target",
  "ssh": "/absolute/pinned/openssh/bin/ssh",
  "identityFile": "/private/operator-key",
  "knownHostsFile": "/private/enrolled-known-hosts",
  "primaries": [{
    "clusterIdentity": "cluster-from-the-authoritative-recovery-set",
    "address": "192.0.2.20",
    "machineID": "0123456789abcdef0123456789abcdef",
    "systemIdentifier": "123456789"
  }]
}
```

Use actual independently enrolled identities; do not copy these examples or
derive the original identity from the replacement. The enrollment must cover
every physical cluster in the selected RecoverySet exactly once, even when
control and DuckLake databases share a cluster. SSH uses explicit pinned known
hosts and identity, disables ambient configuration/agent access, and bounds each
observation. The private authority URL and enrollment files must satisfy the
existing private-file contract.

```sh
leapviewctl host fence-recovery \
  --enrollment-file /private/original-primaries.json \
  --control-url-file /private/recovery-authority-url \
  --recovery-set-id EXACT_RECOVERY_SET_ID

leapviewctl host fence-recovery \
  --enrollment-file /private/original-primaries.json \
  --control-url-file /private/recovery-authority-url \
  --recovery-set-id EXACT_RECOVERY_SET_ID --check
```

The authority must remain available independently of each original PostgreSQL
system being fenced. The command queries its system identifier and rejects an
authority hosted on an enrolled original system. This is an explicit operational
prerequisite, not permission to add a third production host or a replacement for
retained-authority design. A two-host installation whose only authority is on the
lost database still needs that recovery composition qualified before admission.

The first command durably fences originals. The second only checks existing
fences. A partial failure retains completed fences; retry the same frontier.
Another recovery set cannot adopt the marker. There is deliberately no automatic
unfence command. Before any separately reviewed manual unfence, the replacement
must be abandoned or independently fenced so only one writer can exist.

Managed controllers compose `providerrestore.NewManaged` with the same
`SSHPrimaryFence`. They reuse the existing RecoverySet, recovery occurrence,
checkpoint, verifier, and handoff contracts. Physical fencing is freshly checked
before effects and publication/admission, after provider effects, and before
returning a previously completed operation. Lease ownership is checked again
after a potentially slow remote fence observation. Generic provider component
tests using `providerrestore.New` do not establish managed fencing.

## Independent managed-local recovery authority

`leapviewctl host restore-managed` composes the retained pgBackRest/WAL frontier,
exact encrypted Restic snapshots, native PostgreSQL TLS readback, runtime
credentials and the original-writer fence. It requires a separately available
PostgreSQL authority containing the prepared RecoverySet and pending recovery
occurrence. The source database cannot supply that authority after it is fenced.
Use the following order before attempting a managed-local restore.

1. Provision an empty database on an independent PostgreSQL system. Retain its
   actual `pg_control_system()` system identifier and explicit TLS trust, along
   with the original system identifier. The bootstrap login must be authorized
   to create the dedicated roles and database schemas. This command does not
   provision another host or PostgreSQL server:

   ```sh
   leapviewctl host init-managed-recovery-authority \
     --input /private/managed-authority-init.json
   ```

   The strict input document has `schemaVersion: 1`, `originalSystemIdentifier`,
   `ownerRole`, `bootstrap`, `operator`, and `receiptFile`. Both connection objects
   contain `urlFile`, `rootCaFile`, `role`, and `systemIdentifier`; they must name
   the same independent endpoint/database, system identifier and CA bytes. URL
   files contain an explicit password and `sslmode=verify-full`. Use an owner
   role named `leapview_recovery_owner` and an operator named
   `leapview_recovery_operator`, optionally with a lowercase identifier suffix
   beginning with `_` to separate authorities on one system.

   Initialization installs the maintained jobs, recovery ledger and RecoverySet
   schemas in one transaction, with a non-login owner and dedicated login
   operator. The operator can maintain the recovery occurrence and frontier;
   it cannot assume the owner, create application objects, edit the authority
   marker, delete the ledger or use job-history authority. Ordinary application
   roles receive no access to these independent schemas. Existing application
   data, previously claimed dedicated roles or a conflicting initialization
   receipt cause refusal. Retry the exact input and retained operator password
   if acknowledgement or receipt persistence is interrupted. A different schema
   digest or identity requires a separately reviewed authority migration.

2. While the source instance is quiescent, prepare and retain one exact source
   RecoverySet through the existing administrative recovery command. Retain its
   full frontier digest, finite live retention-root identity and expiration,
   immutable artifact identity and source system identifier. Then enroll that
   prepared checkpoint before fencing the source:

   ```sh
   leapviewctl host enroll-managed-recovery \
     --input /private/managed-recovery-enrollment.json
   ```

   The strict input has `schemaVersion: 1`, `source`, `authority`, `request`,
   and `receiptFile`. `source` and `authority` use the connection fields above.
   The request binds `instanceHome`, `recoverySetId`, `frontierDigest`,
   `retentionRootId`, `sourceSystemId`, `authoritySystemId`, `artifactIdentity`,
   `actor`, `plannedAt`, and `expiresAt`. Use canonical UTC timestamps with no
   precision finer than microseconds; the retained source hold must cover the
   entire requested interval. The command holds the instance-home lock and
   authenticates both independent systems. It reads the exact prepared source
   set and live retention row, then atomically installs unchanged canonical set
   bytes and a pending restore intent in the independent authority. It imports
   neither source validation success nor publication authority. Retain the
   private receipt; an exact retry recovers the same receipt after interruption.

3. Run the restore under the provisioned unprivileged PostgreSQL/recovery owner
   using a strict `managed-local` input document:

   ```sh
   leapviewctl host restore-managed \
     --input /private/managed-recovery-restore.json
   ```

   Bind the enrollment receipt and its occurrence, set, instance home and
   immutable artifact without substitution. Retain explicit paths and digests
   for pinned PostgreSQL, pgBackRest, bubblewrap, provider configuration and TLS
   server credentials; the exact backup label, PostgreSQL system identity,
   timeline and WAL restore target; every full Restic snapshot ID and content
   manifest; the native DuckLake closure; private retained runtime credentials
   and keyring; and the original-primary SSH enrollment. Each restore destination
   must preserve the enrolled root location. An off-host SFTP repository also
   requires the exact address, port, user, pinned SSH executable, private
   identity file and enrolled known-hosts file. SSH ignores ambient config,
   agents and passwords and requires the pinned host key.

   Supply the closure's value JSON, including `catalog_id`, `snapshot_id`,
   `object_root`, `relation_namespace`, sorted `relations`/`objects`, and their
   three digests. Input decoding reconstructs the omitted canonical documents
   and verifies the supplied digests; invented canonical-byte fields are rejected.

   The coordinator claims the exact pending occurrence and maintains its lease
   while restoring. It verifies fencing around provider effects and final
   publication, confines native PostgreSQL restore/readback, verifies every
   retained file and the production object-store envelope, and publishes only
   the exact successful validation attempt. Missing or conflicting identities,
   credentials, files, trust or lease ownership fail closed. Completed retries
   still check original-writer fencing. This command does not reopen traffic or
   automatically remove the original fence.

Input documents and credential files must be owned, private regular files;
receipt and secret directories must be private canonical paths. Receipts cannot
overwrite inputs or credentials. The initializer and enrollment inputs are
bounded to 64 KiB; the restore input is bounded to 16 MiB. Unknown fields,
duplicate JSON keys and trailing documents are rejected. Keep private inputs
and encrypted repository keys out of public evidence, PR descriptions and logs.

## Qualification boundaries

The existing NixOS boot fixture now includes an off-host component recovery
journey after installed-host update and rollback. It retains an encrypted
pgBackRest backup and named WAL restore point on the other guest, retains an
exact Restic snapshot off-host, advances/corrupts the source after the frontier,
fences and reboots the original, and restores the selected database and files on
the replacement. SQL assertions reconcile the job, publication acknowledgment,
and file digest against the pre-damage values before a replacement write.
It records recovery elapsed time and provider identities.

Run it with `task managed:hosts:boot-test`, or the managed scaffold workflow's
existing `boot_test` dispatch input. The pure host-fence tests run on scaffold
PRs; Go regressions cover exact canonical RecoverySet binding, missing cluster
coverage, stale receipts, lost fences before publication, controller retries,
explicit SSH trust, and independently available authority.

`TestManagedRecoveryProductionNativeReadback` additionally creates the real
first-source publication through production HTTP routes, restarts and queries
it, initializes an independent TLS authority, and enrolls the retained prepared
frontier using its dedicated operator. It checks refusal of an unprepared set,
transaction rollback, exact retries and altered enrollment identities. With
explicit pinned `LEAPVIEW_TEST_MANAGED_RESTIC`,
`LEAPVIEW_TEST_MANAGED_POSTGRES_BIN`, `LEAPVIEW_TEST_MANAGED_PGBACKREST` and
`LEAPVIEW_TEST_MANAGED_BWRAP` inputs, it also performs encrypted backup/restore of
the actual native DuckLake files and serving-artifact envelope, plus a physical
PostgreSQL backup and confined TLS/WAL restore of the actual publication and
ownership, with the retained private keyring verified separately. The provider
subtests skip when these tools are absent;
an ordinary test run alone is not native restore qualification. This journey
proves the concrete provider/readback and enrollment components; the full
coordinator's original-host fence remains a separate qualified boundary.

The VM journey uses disposable test data and a copied encrypted POSIX repository;
it does not establish production S3 retention, real application credential/key
recovery, Hetzner fencing/rescue, UEFI, an actual customer RecoverySet restore, or
full managed-profile acceptance. Its fixture frontier is explicitly separate
from the canonical RecoverySet authority enforced by the Go coordinator. D09 and
D13 remain open until their remaining real-provider/application acceptance
evidence is retained and reviewed.
