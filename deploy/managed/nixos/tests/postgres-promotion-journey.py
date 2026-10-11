# Real installed database-module component test. Its disposable POSIX backup,
# synthetic frontier ID and fixture roles do not establish managed qualification.
import hashlib
import json
import shlex
import time


def write(machine, path, value, owner="root", group="root", mode="0600"):
    machine.succeed("umask 077; printf %s " + shlex.quote(value) + " > " + shlex.quote(path)
                    + "; chown " + owner + ":" + group + " " + shlex.quote(path)
                    + "; chmod " + mode + " " + shlex.quote(path))


def sql(machine, statement):
    return machine.succeed("sudo -u postgres psql -XAt -v ON_ERROR_STOP=1 -d postgres -c "
                           + shlex.quote(statement)).strip()


def digest(domain, value):
    return "sha256:" + hashlib.sha256((domain + value).encode()).hexdigest()


def helper(check=False, request_path="/home/recovery-fixture/request.json"):
    return ("sudo -u recovery-fixture /run/wrappers/bin/sudo -n -- "
            "/run/current-system/sw/bin/leapview-postgres-promote"
            + (" --check" if check else "") + " < " + request_path)


started = time.monotonic()
source.start()
# The driver otherwise starts QEMU with -no-reboot and exits at the later
# reboot assertion before retained promotion state can be checked.
replacement.start(allow_reboot=True)
for machine in (source, replacement):
    machine.wait_for_unit("sshd.service", timeout=300)
    machine.succeed("install -d -m 0700 -o recovery-fixture -g recovery-fixture /home/recovery-fixture")
    machine.succeed("openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj /CN=database.local "
                    "-addext subjectAltName=DNS:database.local,IP:127.0.0.1 "
                    "-keyout /var/lib/leapview-postgres-tls/server.key "
                    "-out /var/lib/leapview-postgres-tls/server.crt")
    machine.succeed("chown postgres:postgres /var/lib/leapview-postgres-tls/server.*; "
                    "chmod 0400 /var/lib/leapview-postgres-tls/server.key; "
                    "touch /var/lib/leapview-backup-secrets/pgbackrest.conf; "
                    "chown root:pgbackrest /var/lib/leapview-backup-secrets/pgbackrest.conf; "
                    "chmod 0640 /var/lib/leapview-backup-secrets/pgbackrest.conf; "
                    "systemctl reset-failed postgresql.service; systemctl start postgresql.service")
    machine.wait_for_unit("postgresql.service", timeout=300)

with subtest("actual module owner has only fixed helper and receipt-read authority"):
    replacement.succeed("test $(stat -c '%a:%U:%G' /var/lib/leapview-recovery-adoption) = 750:root:leapview-recovery-adoption")
    replacement.fail("sudo -u recovery-fixture test -r /var/lib/postgresql/18/PG_VERSION")
    replacement.fail("sudo -u recovery-fixture /run/wrappers/bin/sudo -n -- "
                     "/run/current-system/sw/bin/leapview-postgres-promote --data /tmp/foreign")
    replacement.fail("sudo -u unrelated-fixture /run/wrappers/bin/sudo -n -- "
                     "/run/current-system/sw/bin/leapview-postgres-promote --check")
    replacement.succeed("test ! -e /var/lib/leapview-recovery-adoption/postgresql-promotion-intent.json")

with subtest("exact physical backup and archived WAL preserve the selected acknowledged rows"):
    sql(source, "CREATE TABLE retained_frontier(value integer); INSERT INTO retained_frontier VALUES (1)")
    sql(source, "CREATE ROLE component_runtime LOGIN PASSWORD 'fixture-only-secret'; "
                "CREATE ROLE component_maintenance LOGIN PASSWORD 'fixture-only-maintenance'")
    sql(source, "CREATE DATABASE leapview_control")
    sql(source, "CREATE DATABASE leapview_ducklake")
    source.succeed("sudo -u postgres psql -X -v ON_ERROR_STOP=1 -d leapview_control "
                   "-c 'CREATE TABLE acknowledged_rows(value integer); INSERT INTO acknowledged_rows VALUES (1); "
                   "GRANT SELECT ON acknowledged_rows TO component_runtime,component_maintenance'")
    system = sql(source, "SELECT system_identifier FROM pg_control_system()")
    timeline = int(sql(source, "SELECT timeline_id FROM pg_control_checkpoint()"))
    backup_command = "sudo -u postgres " + pgbackrest + " --stanza=default "
    source.succeed(backup_command + "stanza-create", timeout=120)
    try:
        source.succeed(backup_command + "--start-fast --type=full backup", timeout=180)
    except Exception:
        # Retain the original bounded failure. Inspect only disposable fixture
        # state, so hosted KVM evidence can distinguish archive state from TCG.
        diagnostics = [
            "sudo -u postgres psql -XAt -d postgres -c 'SELECT archived_count,last_archived_wal,failed_count,last_failed_wal FROM pg_stat_archiver'",
            "find /var/lib/promotion-component/repo -maxdepth 6 -type f -printf '%P\\n' | head -40",
            "ps -eo pid,ppid,stat,wchan:24,args | grep -E '[p]gbackrest|[p]ostgres' | head -40",
        ]
        for command in diagnostics:
            try:
                status, output = source.execute(command, timeout=30)
                print("fixture backup diagnostic", status, output[:4096])
            except Exception:
                print("fixture backup diagnostic unavailable")
        raise
    backup = json.loads(source.succeed(backup_command + "--output=json info"))[0]["backup"][-1]["label"]
    source.succeed("sudo -u postgres psql -X -v ON_ERROR_STOP=1 -d leapview_control "
                   "-c 'INSERT INTO acknowledged_rows VALUES (2)'")
    target = sql(source, "SELECT pg_current_wal_insert_lsn()")
    sql(source, "SELECT pg_switch_wal()")
    source.succeed(backup_command + "check", timeout=120)
    source.succeed("sudo -u postgres psql -X -v ON_ERROR_STOP=1 -d leapview_control "
                   "-c 'INSERT INTO acknowledged_rows VALUES (3)'")
    sql(source, "SELECT pg_switch_wal()")
    source.succeed(backup_command + "check", timeout=120)

    replacement.succeed("install -d -m 0700 /root/.ssh; install -m 0600 " + fixture_ssh_key + " /root/.ssh/id_ed25519")
    host_key = source.succeed("cat /etc/ssh/ssh_host_ed25519_key.pub").strip()
    write(replacement, "/root/.ssh/known_hosts", "192.168.1.2 " + host_key + "\n")
    replacement.succeed("scp -rq -o StrictHostKeyChecking=yes -o IdentitiesOnly=yes "
                        "-o IdentityAgent=none -i /root/.ssh/id_ed25519 "
                        "root@192.168.1.2:/var/lib/promotion-component/repo /var/lib/promotion-component/", timeout=180)
    replacement.succeed("chown -R postgres:postgres /var/lib/promotion-component")
    # Stop the disposable source before starting its physical copy. This local
    # component test does not substitute for retained original fencing evidence.
    source.succeed("systemctl stop postgresql.service")
    source.fail("systemctl is-active postgresql.service")
    replacement.succeed("systemctl stop postgresql.service; rm -rf /var/lib/postgresql/18")
    replacement.succeed("sudo -u postgres " + pgbackrest + " --stanza=default --set=" + shlex.quote(backup)
                        + " --type=lsn --target=" + shlex.quote(target) + " --target-action=pause restore", timeout=180)
    # Model the completed stopped-provider prerequisite using the actual module
    # service. The promotion helper must replace this restored auto.conf itself.
    with_config = "listen_addresses='127.0.0.1'\nrecovery_target_lsn='" + target + "'\nrecovery_target_timeline='" + str(timeline) + "'\nrecovery_target_action='pause'\nrestore_command='" + pgbackrest + " --stanza=default archive-get %f %p'\n"
    write(replacement, "/var/lib/postgresql/18/postgresql.auto.conf", with_config, "postgres", "postgres", "0600")
    replacement.succeed("systemctl start postgresql.service")
    replacement.wait_until_succeeds("sudo -u postgres psql -XAt -c 'SELECT pg_is_in_recovery() AND pg_is_wal_replay_paused()' | grep -qx t", timeout=120)
    replacement.succeed("systemctl stop postgresql.service")
    replacement.succeed("sudo -u postgres " + postgres_bin + "/pg_controldata /var/lib/postgresql/18 | grep 'Database cluster state:.*shut down in recovery'")
    # A restored command/include must not survive entry into production service.
    write(replacement, "/var/lib/postgresql/18/postgresql.auto.conf", "include='/tmp/untrusted-restored-config'\nshared_preload_libraries='untrusted'\n", "postgres", "postgres", "0600")
    request = {"schemaVersion": 1, "targetID": "installed-promotion-component", "recoverySetID": "component-frontier",
               "frontierDigest": digest("component-only:", backup + target),
               "sourceMachineID": source.succeed("cat /etc/machine-id").strip(),
               "replacementMachineID": replacement.succeed("cat /etc/machine-id").strip(),
               "systemIdentifier": system, "targetLSN": target, "timeline": timeline,
               "dataDirectoryDigest": digest("leapview-managed-recovery-data:", "/var/lib/postgresql/18")}
    assert request["sourceMachineID"] != request["replacementMachineID"]
    write(replacement, "/home/recovery-fixture/request.json", json.dumps(request), "recovery-fixture", "recovery-fixture")

with subtest("installed fixed sudo helper promotes only the exact module-owned private service"):
    assert replacement.succeed("stat -c %a:%U:%G /var/lib/postgresql/18").strip() == "750:postgres:postgres"
    receipt = json.loads(replacement.succeed(helper(), timeout=180))
    assert replacement.succeed("stat -c %a:%U:%G /var/lib/postgresql/18").strip() == "750:postgres:postgres"
    assert receipt["status"] == "promoted-loopback-only"
    assert receipt["activationQualified"] is False and receipt["fullManagedProfileQualified"] is False
    assert receipt["systemIdentifier"] == system and receipt["targetLSN"] == target
    assert receipt["promotedTimeline"] == timeline + 1
    assert request["sourceMachineID"] not in json.dumps(receipt) and request["replacementMachineID"] not in json.dumps(receipt)
    assert sql(replacement, "SELECT NOT pg_is_in_recovery()") == "t"
    assert sql(replacement, "SHOW listen_addresses") == "127.0.0.1"
    assert sql(replacement, "SHOW shared_preload_libraries") == ""
    assert json.loads(replacement.succeed(helper(check=True))) == receipt
    assert json.loads(replacement.succeed(helper())) == receipt
    replacement.succeed("sudo -u recovery-fixture cat /var/lib/leapview-recovery-adoption/postgresql-promotion.json > /dev/null")
    replacement.fail("sudo -u recovery-fixture test -w /var/lib/leapview-recovery-adoption/postgresql-promotion.json")
    replacement.fail("sudo -u unrelated-fixture cat /var/lib/leapview-recovery-adoption/postgresql-promotion.json")
    replacement.fail("sudo -u recovery-fixture cat /var/lib/leapview-recovery-adoption/postgresql-promotion-intent.json")
    replacement.fail("sudo -u recovery-fixture readlink -f /var/lib/postgresql/18/postgresql.conf")
    source.fail("nc -z -w 3 192.168.1.1 5432")

with subtest("unprivileged runtime and maintenance readback use actual module HBA and verified TLS"):
    ca = replacement.succeed("cat /var/lib/leapview-postgres-tls/server.crt")
    write(replacement, "/home/recovery-fixture/ca.crt", ca, "recovery-fixture", "recovery-fixture")
    for role, password in [("component_runtime", "fixture-only-secret"), ("component_maintenance", "fixture-only-maintenance")]:
        connection = "host=database.local hostaddr=127.0.0.1 dbname=leapview_control user=" + role + " sslmode=verify-full sslrootcert=/home/recovery-fixture/ca.crt"
        query = "SELECT string_agg(value::text,',' ORDER BY value) FROM acknowledged_rows"
        command = "sudo -u recovery-fixture env PGPASSWORD=" + password + " psql -XAt -v ON_ERROR_STOP=1 " + shlex.quote(connection) + " -c " + shlex.quote(query)
        assert replacement.succeed(command).strip() == "1,2"
        replacement.fail(command.replace("sslmode=verify-full", "sslmode=disable"))

with subtest("wrong frontier cannot disturb exact completion and reboot preserves private ancestry"):
    write(replacement, "/home/recovery-fixture/wrong.json", json.dumps(dict(request, targetLSN="0/1")), "recovery-fixture", "recovery-fixture")
    replacement.fail(helper(request_path="/home/recovery-fixture/wrong.json"))
    replacement.succeed("systemctl is-active postgresql.service")
    replacement.reboot()
    replacement.wait_for_unit("postgresql.service", timeout=300)
    assert replacement.succeed("stat -c %a:%U:%G /var/lib/postgresql/18").strip() == "750:postgres:postgres"
    replacement.fail("sudo -u recovery-fixture test -r /var/lib/postgresql/18/PG_VERSION")
    assert sql(replacement, "SELECT pg_last_wal_replay_lsn() IS NULL") == "t"
    assert json.loads(replacement.succeed(helper(check=True))) == receipt
    assert json.loads(replacement.succeed(helper())) == receipt
    assert sql(replacement, "SHOW listen_addresses") == "127.0.0.1"
    source.fail("nc -z -w 3 192.168.1.1 5432")
    source.fail("systemctl is-active postgresql.service")
    print(json.dumps({"scope": "installed PostgreSQL promotion components", "activationQualified": False,
                      "fullManagedProfileQualified": False, "durationSeconds": round(time.monotonic() - started, 3)}, sort_keys=True))
