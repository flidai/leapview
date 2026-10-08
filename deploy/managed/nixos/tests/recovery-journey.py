# Executed inside boot.nix's isolated NixOS driver, after the installed-host
# update/rollback journey. This qualifies recovery components, not a customer
# profile: the disposable fixture uses a copied encrypted POSIX repository, not
# the production S3 retention/credential/provider account.
import hashlib
import json
import shlex
import time


def recovery_write(machine, path, text):
    machine.succeed("umask 077; printf %s " + shlex.quote(text) + " > " + shlex.quote(path))


def recovery_sql(machine, sql, restored=False):
    connection = "-h /var/lib/recovery-test/socket -p 55432 " if restored else ""
    return machine.succeed("sudo -u postgres psql -XAt -v ON_ERROR_STOP=1 " + connection
                           + "-d leapview_control -c " + shlex.quote(sql)).strip()


with subtest("encrypted database and home backups bind one recovery frontier"):
    # Reuse the driver's explicitly pinned SSH identity; no host agent/context.
    for machine, address in [(app, "192.168.1.1"), (database, "192.168.1.2")]:
        machine.succeed("install -d -m 700 /root/.ssh /var/lib/recovery-test")
        operator.succeed("scp -q /root/.ssh/id_ed25519 /root/.ssh/known_hosts /root/.ssh/config root@"
                         + address + ":/root/.ssh/")
    app.succeed("install -d -m 700 /var/lib/leapview/recovery-fixture")
    recovery_write(app, "/var/lib/leapview/recovery-fixture/upload.csv", "id,amount\n1,42\n")
    file_digest = app.succeed("sha256sum /var/lib/leapview/recovery-fixture/upload.csv").split()[0]
    recovery_sql(database, "CREATE TABLE recovery_frontier (job_id text PRIMARY KEY, job_state text NOT NULL, "
                 "publication_id text NOT NULL, acknowledgment text NOT NULL, file_digest text NOT NULL)")
    recovery_sql(database, "INSERT INTO recovery_frontier VALUES "
                 "('job-before', 'completed', 'publication-before', 'ack-before', '" + file_digest + "')")

    database.succeed("install -d -m 700 -o postgres -g postgres /var/lib/recovery-test/postgres")
    config = "[global]\nrepo1-path=/var/lib/recovery-test/postgres\nrepo1-cipher-type=aes-256-cbc\nrepo1-retention-full=2\n" \
             "[default]\npg1-path=/var/lib/postgresql/18\n"
    recovery_write(database, "/var/lib/recovery-test/pgbackrest.conf", config)
    # Generate the encryption secret in the guest and never return it to logs.
    database.succeed(r"sed -i '/^\[global\]/a repo1-cipher-pass='$(openssl rand -hex 32) /var/lib/recovery-test/pgbackrest.conf")
    database.succeed("chown postgres:postgres /var/lib/recovery-test /var/lib/recovery-test/pgbackrest.conf")
    pgbackrest = "sudo -u postgres pgbackrest --config=/var/lib/recovery-test/pgbackrest.conf --stanza=default "
    recovery_sql(database, "ALTER SYSTEM SET archive_command='/run/current-system/sw/bin/pgbackrest --config=/var/lib/recovery-test/pgbackrest.conf --stanza=default archive-push %p'")
    recovery_sql(database, "SELECT pg_reload_conf()")
    database.succeed(pgbackrest + "stanza-create", timeout=120)
    database.succeed(pgbackrest + "--type=full backup", timeout=300)
    backup = json.loads(database.succeed(pgbackrest + "--output=json info"))[0]["backup"][-1]["label"]

    app.succeed("openssl rand -hex 32 > /var/lib/recovery-test/restic-password; chmod 600 /var/lib/recovery-test/restic-password")
    restic = "restic --password-file /var/lib/recovery-test/restic-password --repo /var/lib/recovery-test/home-repo "
    app.succeed(restic + "init")
    entries = [json.loads(line) for line in app.succeed(restic + "backup --json /var/lib/leapview/recovery-fixture").splitlines()]
    snapshot = next(value["snapshot_id"] for value in entries if value.get("message_type") == "summary")
    recovery_sql(database, "SELECT pg_create_restore_point('managed_recovery_frontier')")
    recovery_sql(database, "SELECT pg_switch_wal()")
    database.succeed(pgbackrest + "check", timeout=120)

    # Advance all three sides after the selected frontier. Restoring 'latest'
    # instead of either exact identity must fail the later state assertions.
    recovery_sql(database, "UPDATE recovery_frontier SET job_state='failed', acknowledgment='ack-after'; "
                 "INSERT INTO recovery_frontier VALUES ('job-after','queued','publication-after','ack-after','wrong')")
    recovery_write(app, "/var/lib/leapview/recovery-fixture/upload.csv", "corrupted-after-frontier\n")
    app.succeed(restic + "backup /var/lib/leapview/recovery-fixture")
    recovery_sql(database, "SELECT pg_switch_wal()")
    database.succeed(pgbackrest + "check", timeout=120)
    # Complete the off-host copies before fencing or deleting anything.
    database.succeed("scp -qr /var/lib/recovery-test/postgres /var/lib/recovery-test/pgbackrest.conf root@192.168.1.1:/var/lib/recovery-test/", timeout=180)
    app.succeed("scp -qr /var/lib/recovery-test/home-repo root@192.168.1.2:/var/lib/recovery-test/", timeout=180)
    app.succeed("rm -rf /var/lib/recovery-test/home-repo")

    identity = {
        "schemaVersion": 1, "targetID": "nixos-disposable-recovery", "recoverySetID": "component-frontier",
        "clusterIdentity": "installed-postgresql",
        "machineID": database.succeed("cat /etc/machine-id").strip(),
        "systemIdentifier": recovery_sql(database, "SELECT system_identifier FROM pg_control_system()"),
    }
    frontier = {"backup": backup, "restorePoint": "managed_recovery_frontier", "resticSnapshot": snapshot,
                "fileDigest": file_digest, "publication": "publication-before", "job": "job-before", "ack": "ack-before"}
    # This fixture frontier binds the host fence to exact provider identities.
    # The Go managed coordinator separately requires the canonical RecoverySet
    # digest; this fixture never substitutes this envelope for that authority.
    identity["frontierDigest"] = "sha256:" + hashlib.sha256(json.dumps(frontier, sort_keys=True).encode()).hexdigest()
    recovery_write(operator, "/root/recovery-fence.json", json.dumps(identity))
    fence_command = "ssh root@192.168.1.2 /run/current-system/sw/bin/leapview-postgres-fence"
    operator.fail(fence_command + " --check < /root/recovery-fence.json")
    started = time.monotonic()
    receipt = json.loads(operator.succeed(fence_command + " < /root/recovery-fence.json"))
    assert receipt == {"identity": identity, "postgresqlStopped": True, "restartFenced": True}

with subtest("the original writer remains fenced through restart and installed-disk reboot"):
    database.succeed("systemctl start postgresql.service")
    database.fail("systemctl is-active postgresql.service")
    app.fail("PGPASSWORD=disposable-test-only psql -XAt \"" + connection + "\" -c 'select 1'")
    database.reboot()
    database.wait_for_unit("sshd.service", timeout=600)
    database.fail("systemctl is-active postgresql.service")
    operator.succeed(fence_command + " --check < /root/recovery-fence.json")
    changed = dict(identity, recoverySetID="another-operation")
    recovery_write(operator, "/root/foreign-fence.json", json.dumps(changed))
    operator.fail(fence_command + " < /root/foreign-fence.json")

with subtest("replacement recovers exact database, jobs, files and publication acknowledgment"):
    app.succeed("install -d -m 700 -o postgres -g postgres /var/lib/recovery-test/restored-pg /var/lib/recovery-test/socket")
    app.succeed("sed -i 's|pg1-path=/var/lib/postgresql/18|pg1-path=/var/lib/recovery-test/restored-pg|' /var/lib/recovery-test/pgbackrest.conf")
    app.succeed("chown -R postgres:postgres /var/lib/recovery-test/postgres /var/lib/recovery-test/pgbackrest.conf; chown postgres:postgres /var/lib/recovery-test")
    app.succeed(pgbackrest + "--set=" + shlex.quote(backup) + " --type=name --target=managed_recovery_frontier --target-action=promote restore", timeout=300)
    # NixOS's original postgresql.conf links its installed system closure. The
    # replacement must use its own runtime configuration, not depend on that
    # original closure or TLS files. This component probe is local peer-only;
    # production replacement TLS/admission is a separate qualified boundary.
    app.succeed("rm -f /var/lib/recovery-test/restored-pg/postgresql.conf /var/lib/recovery-test/restored-pg/pg_hba.conf")
    recovery_write(app, "/var/lib/recovery-test/restored-pg/postgresql.conf",
                   "hba_file='/var/lib/recovery-test/restored-pg/pg_hba.conf'\n")
    recovery_write(app, "/var/lib/recovery-test/restored-pg/pg_hba.conf", "local all postgres peer\n")
    app.succeed("chown postgres:postgres /var/lib/recovery-test/restored-pg/postgresql.conf /var/lib/recovery-test/restored-pg/pg_hba.conf")
    try:
        # The merged system profile links binaries without PostgreSQL's full
        # share tree. Its launcher must retain the locked package prefix to
        # resolve postgres and timezonesets, as the production service does.
        app.succeed("sudo -u postgres " + shlex.quote(recovery_postgres_bin + "/pg_ctl")
                    + " -D /var/lib/recovery-test/restored-pg -l /var/lib/recovery-test/postgresql.log "
                    "-o \"-p 55432 -k /var/lib/recovery-test/socket -c listen_addresses=127.0.0.1 -c ssl=off -c archive_mode=off\" -w start", timeout=120)
        # pg_ctl readiness can mean hot-standby read-only acceptance while PITR
        # is still promoting. Replacement writes require completed promotion.
        app.wait_until_succeeds("test \"$(sudo -u postgres psql -XAt -v ON_ERROR_STOP=1 "
                                "-h /var/lib/recovery-test/socket -p 55432 -d leapview_control "
                                "-c 'SELECT NOT pg_is_in_recovery()')\" = t", timeout=120)
    except Exception:
        print(app.succeed("tail -n 80 /var/lib/recovery-test/postgresql.log"))
        raise
    restored = recovery_sql(app, "SELECT job_id,job_state,publication_id,acknowledgment,file_digest FROM recovery_frontier ORDER BY job_id", restored=True)
    assert restored == "job-before|completed|publication-before|ack-before|" + file_digest, restored
    remote_restic = "restic --password-file /var/lib/recovery-test/restic-password --repo sftp:root@192.168.1.2:/var/lib/recovery-test/home-repo "
    app.succeed(remote_restic + "check", timeout=180)
    app.succeed(remote_restic + "restore " + shlex.quote(snapshot) + " --target /var/lib/recovery-test/restored-home", timeout=180)
    assert app.succeed("sha256sum /var/lib/recovery-test/restored-home/var/lib/leapview/recovery-fixture/upload.csv").split()[0] == file_digest
    operator.succeed(fence_command + " --check < /root/recovery-fence.json")
    recovery_sql(app, "INSERT INTO recovery_frontier VALUES ('replacement-job','queued','replacement-publication','unacknowledged','replacement')", restored=True)
    database.fail("systemctl is-active postgresql.service")
    print(json.dumps({"qualification": "isolated NixOS recovery components", "frontier": frontier,
                      "fence": receipt, "recoverySeconds": round(time.monotonic() - started, 3)}, sort_keys=True))
