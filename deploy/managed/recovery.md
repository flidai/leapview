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

The VM journey uses disposable test data and a copied encrypted POSIX repository;
it does not establish production S3 retention, real application credential/key
recovery, Hetzner fencing/rescue, UEFI, an actual customer RecoverySet restore, or
full managed-profile acceptance. Its fixture frontier is explicitly separate
from the canonical RecoverySet authority enforced by the Go coordinator. D09 and
D13 remain open until their remaining real-provider/application acceptance
evidence is retained and reviewed.
