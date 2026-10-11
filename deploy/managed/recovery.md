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

## Bounded replacement-host qualification

On the actual replacement, `leapviewctl host qualify-managed-recovery` uses the
same strict private restore input, independent authority and concrete coordinator
as `restore-managed`. Run it under the provisioned unprivileged recovery owner
after initialization, exact source enrollment and explicit original fencing:

```sh
leapviewctl host qualify-managed-recovery \
  --input /private/managed-recovery-restore.json \
  --output /private/managed-recovery-preactivation.json
```

This is a fresh exercise: the authoritative set must remain prepared and its
occurrence pending, unexpired and unattempted. PostgreSQL and every enrolled
object destination must be absent, nonoverlapping canonical paths with existing
private parents. The output must be a new private path outside restored provider
state. The runner reads `/etc/machine-id` on the replacement, refuses the enrolled
original machine identity, authenticates the independently enrolled PostgreSQL
authority, and holds the actual instance-home lock throughout execution.

The command measures a fresh coordinator restore, a completed retry which must
return the identical retained report without another restore, and a separate
fresh stopped-provider admission. Admission must bind the exact restored report,
frontier, artifact, published validation and original-writer fence. Only success
retains a bounded receipt with those public identities, a digest of replacement
machine identity and stage durations; it includes no raw machine ID, credentials,
provider paths or provider output. Both `activationQualified` and
`fullManagedProfileQualified` remain false. These timings cover this command's
restore/replay/admission interval; they do not measure end-to-end outage or RTO.

Failure does not erase provider state, release the original fence, reset an
attempted occurrence or authorize traffic. Inspect private retained state and use
the existing `restore-managed` completed retry or `admit-managed-recovery` for
recovery; do not remove destinations or recreate authority to obtain a passing
receipt. Another fresh timed exercise requires a separately prepared and enrolled
frontier with an unattempted occurrence and empty replacement destinations.

This entrypoint supplies an executable preactivation milestone, not a completed
protected two-host deployment qualification. A protected run still needs the
actual enrolled original and replacement hosts, independent authority, pinned
release/controller inputs, off-host encrypted backup access, exact provider
frontier/manifests, explicit SSH/TLS trust and retained runtime credentials.
The full D13 gate additionally requires installed deployment and activation,
fresh-pool reconciliation of files/jobs/acknowledgments, replacement writes,
fresh-pool rebuild and major maintenance with measured bounds. Unit fixtures and
the component VM journey below do not establish those results.

## Promoted local adoption with ingress closed

After completed managed restore and preactivation qualification,
`leapviewctl host adopt-managed-recovery` promotes the replacement PostgreSQL
service and atomically imports the independent authority's exact published set,
passed validation attempt and canonical evidence into restored control storage.
It does not start the application, reconcile jobs/uploads or admit traffic.

Provision the explicit unprivileged recovery owner and configure
`leapview.database.recoveryOwner` on the replacement database host. The module
grants only the fixed promotion helper and a dedicated receipt-read group, never
`postgres` membership. Set `services.postgresql.dataDir` to the exact restored
destination, already owned by the configured PostgreSQL service user with mode
`0700` or the pinned module’s `0750`, owned by both the fixed `postgres` user
and `postgres` group; writable group access and world access are rejected. The
recovery owner remains outside that group. Restore ownership and cross-host file placement remain explicit site
inputs. The helper rejects an unrelated destination, a live first-use cluster,
another original/replacement machine, another system ID, linked or nondefault
tablespaces, another frontier or another module generation. It neither moves nor
chowns trees, rewrites rollback generations nor removes the original fence.

Only the module's reviewed immutable configuration and pinned PostgreSQL and
pgBackRest tools are used. Restored `postgresql.auto.conf` is replaced with
bounded exact-target settings; preload settings and logical workers are disabled.
PostgreSQL stays **loopback-only** throughout. The module's loopback HBA still
requires SCRAM over TLS for control/DuckLake databases. The controller preserves
the retained URLs' verified TLS names while dialing that same loopback service.
Both retained runtime credentials and an explicit maintenance credential must
authenticate. No endpoint/password or raw machine identity appears in receipts.

Write a mode `0600` replacement input in an existing private directory:

```json
{
  "schemaVersion": 1,
  "maintenance": {
    "urlFile": "/private/recovery/replacement-control-url",
    "rootCaFile": "/private/recovery/retained-postgres-ca.pem",
    "role": "explicit_control_maintenance_role",
    "systemIdentifier": "exact_retained_source_system_identifier"
  }
}
```

The URL file contains a password-bearing `postgres://` URL with
`sslmode=verify-full`, the same retained control database/server name/port and
explicit maintenance role. CA bytes must equal the retained credential bundle.
The role needs the existing control-repository mutation and PostgreSQL
identity/settings read privileges; this command grants none.

```sh
leapviewctl host adopt-managed-recovery \
  --input /private/recovery/managed-input.json \
  --replacement /private/recovery/replacement-input.json \
  --output /private/recovery/adoption-evidence.json
```

The controller holds the actual instance-home lock and freshly observes the
original fence before/after promotion and inside the adoption transaction before
commit. It revalidates completed restore evidence, the exact passed attempt and
fence, native publication/catalog/keyring and every retained file manifest.
Drift rolls back the imported metadata and leaves activation closed. Exact
retries resume matching partial validation state or the same local publication.

Before irreversible promotion, a private root-owned intent binds the request,
module generation, data directory and actual paused replay LSN. PostgreSQL can
pause at a record end beyond its configured target and forget its replay LSN
after primary restart. The helper authenticates the configured target/pause,
persists that observed replay position and verifies the new timeline history's
exact fork point. A crash after promotion but before receipt publication resumes
only from this authenticated intent and actual service ancestry. A changed
generation, missing history or conflicting state requires operator investigation;
an arbitrary already-promoted endpoint cannot qualify.

Receipts always record `activationQualified: false` and
`fullManagedProfileQualified: false`. Native atomic-storage, replay/promotion,
crash/restart, actual module HBA/TLS and isolated UID/GID regression tests cover
this source transition. They do not qualify installed two-host deployment. D13
still requires protected host/provider inputs, application readiness,
jobs/uploads/files reconciliation, fresh pool/catalog rebuild, replacement writes
and the complete two-host maintenance journeys with retained measured evidence.

## Module-owned PostgreSQL restore seam

The unprivileged confined restore writes a recovery-owner-owned tree. Its
validation envelope authenticates the selected catalog/publication and object
manifests, not every PGDATA byte. Moving or chowning that tree into the
PostgreSQL service would therefore widen the trust boundary, including any
writer descriptors retained by the recovery owner.

The replacement database module offers an explicit alternative:
`leapview.database.recoveryRestore = true`, together with the provisioned
`recoveryOwner`. It installs `leapview-postgres-restore` with only `fresh`,
`restore`, `check`, `start-readback` and `stop-readback` actions. `fresh` checks the
exact absent destination and stopped service without creating PGDATA or intent. The helper accepts a
bounded strict request on stdin identifying the exact target, RecoverySet,
frontier, occurrence, operation, backup set, source/replacement machines,
PostgreSQL system ID, timeline, LSN and module data-directory digest. Paths,
tools, provider configuration and credential files come exclusively from the
reviewed module. The controller remains responsible for independently verifying
the request's authoritative enrollment/frontier and fresh original fence.

In this opt-in mode automatic PostgreSQL boot startup and backup jobs are
disabled. A root materialization condition prevents upstream initdb from
creating an unrelated cluster. The fixed helper restores directly as PostgreSQL
into a new private staging directory, using the module's pgBackRest installation
and root-owned retained credentials. It never imports a caller-owned tree or
changes ownership of existing data. Root intent binds the staging inode, exact
request, module generation and provider configuration before work begins;
the provider supervisor retains the shared promotion lock across controller
death. Existing unrelated PGDATA or service, changed credentials/generation,
foreign retry and unpaused or changed replay fail closed. The helper replaces
restored auto configuration with module-owned loopback-only recovery settings,
checks native identity and replay, and stops the service before publishing its
bounded root receipt. Temporary readback requires that exact completed receipt.

`test:qualification:managed-replacement` runs the native pgBackRest/PITR and
interruption regressions in the existing `managed-recovery` Nix shell. Module
contracts cover opt-in owner permissions, boot suppression and the original
fence. To select this installed path, set the private managed input’s
`postgres.provider` to `module-owned`, retain its exact `frontier`, `destination`
and `metadataSchema`, and omit caller tool/configuration and server TLS key
fields. The default input continues to use the confined provider. Module-owned
restore, completed retry, qualification and admission share the existing
coordinator and independent authority. The recovery owner delegates PGDATA
freshness to the fixed helper; object destinations remain private and disjoint.

The adapter authenticates the root receipt against the current replacement,
module generation, occurrence and backup/WAL frontier, starts only the private
module service for retained TLS runtime readback, then stops and rechecks it.
Published-set admission uses only `check`, so missing state cannot trigger a
new physical restore. Cancellation and failed readback use bounded cleanup;
changed receipts or incomplete cleanup cannot return successful evidence. The
resulting stopped PostgreSQL is reachable by the existing promotion/adoption
command without transferring recovery-owner data to the service owner.

An actual installed separate-owner coordinator journey, independently retained
fence/enrollment/credentials, application/job/upload reconciliation and measured
full two-host recovery remain qualification inputs. These component tests and
receipts establish neither application activation nor the full D13 profile.

## Qualification boundaries

`task managed:hosts:postgres-promotion-test` runs the installed database module's
fixed sudo promotion helper, actual systemd service, TLS/HBA, private receipt
permissions and exact reboot retry in disposable guests. It restores an exact
physical pgBackRest backup/WAL target and checks acknowledged rows before and
after promotion. The managed scaffold's existing host job runs this component
test. Its source is explicitly stopped, and its synthetic frontier and POSIX
repository do not replace retained original fencing, the canonical coordinator,
application reconciliation or the protected managed-profile gate.

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

`nix develop .#managed-recovery -c task test:qualification:managed-replacement`
uses the locked pgBackRest, PostgreSQL, Restic and confinement tools to restore
the real production publication into a separate local database process. It
adopts the component test authority's exact published frontier through
`AdoptPublishedTx`, moves restored files to their unchanged private fixture
paths, and starts a fresh production application with maintenance admission
closed. The bounded local HTTP exercise checks retained query results and
serving identity across another application/pool restart, and compares the
nonempty original publication and completed-job identities and rows. Missing
adoption or the serving artifact prevents admission. Missing DuckLake data
files fail retained manifest verification and the governed query; readiness
checks sealed metadata and does not scan every data-file byte. The Nix development lane
runs this component test with required providers; it does not simulate the
installed promotion receipt or establish independent-host fencing, public
traffic admission, or broad asynchronous job/upload reconciliation.

The VM journey uses disposable test data and a copied encrypted POSIX repository;
it does not establish production S3 retention, real application credential/key
recovery, Hetzner fencing/rescue, UEFI, an actual customer RecoverySet restore, or
full managed-profile acceptance. Its fixture frontier is explicitly separate
from the canonical RecoverySet authority enforced by the Go coordinator. D09 and
D13 remain open until their remaining real-provider/application acceptance
evidence is retained and reviewed.

### Disposable installed coordinator component

`task managed:hosts:coordinator-test` builds the production controller and a
focused actual-publication fixture executable, then runs the pinned NixOS test
driver outside the build sandbox. The fixture completes the real first-source
HTTP publication and durable activation-job acknowledgement before privately
exporting its physical PostgreSQL snapshot, encrypted instance keyring, native
DuckLake closure and immutable Restic snapshots. Its content manifest covers
every exported file. The export and public executable hashes are independent
provenance; no private bytes enter the Nix store.

The disposable original installs that exact snapshot, captures an actual
module-owned pgBackRest backup/WAL target, and invokes production
`PrepareRecovery` to retain the canonical frontier and retention hold. A third
disposable PostgreSQL cluster provides the independent authority. Production
CLI commands initialize that authority, enroll the prepared frontier, establish
a real original-host SSH fence, and qualify fresh replacement restoration,
completed acknowledgement replay and independent admission. The replacement
uses `recoveryRestore = true`, the actual fixed module helper and a recovery
owner outside the PostgreSQL group. Reboot admission must leave PostgreSQL
stopped. Wrong original-machine/frontier substitutions cannot produce a
qualification receipt or materialize PGDATA.

The `installed-coordinator` managed scaffold CI job has its own 60-minute
budget. It retains raw failure diagnostics only in private disposable fixture
files and uploads no private fixture artifacts. A failed local run reports the
owned mode0700 directory for diagnosis; a successful run removes it.

The intended linux/amd64 image reference is pinned to the real retained
`managed-admission-38016533303-1-amd64/binding.json` from release run
[38016533303](https://github.com/flidai/leapview/actions/runs/38016533303).
This component **does not execute that image**. Its intended image/source
identity is reported separately from the actual fixture/controller producer
hashes and source identities. It reuses the authenticated cached DuckDB engine
for the fixture rather than proving the rebuilt-engine release gate.
`activationQualified`, `releaseAdmissionQualified`, and
`fullManagedProfileQualified` remain false. Customer provider credentials,
protected immutable-candidate deployment, actual managed host inventory,
application readiness/acknowledgement reconciliation and the full two-host
qualification remain required before D13 can close.
