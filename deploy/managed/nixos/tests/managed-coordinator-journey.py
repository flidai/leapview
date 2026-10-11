# Driver-only installed component journey. Private publication fixture bytes
# are supplied after Nix builds; this script never fabricates durable authority.
import copy
from datetime import datetime, timedelta, timezone
import importlib.util
import json
import os
from pathlib import Path
import shlex
import tempfile
import time
import uuid

spec = importlib.util.spec_from_file_location("coordinator_fixture", fixture_helpers)
assert spec is not None and spec.loader is not None
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)
private = Path(os.environ["LEAPVIEW_TEST_MANAGED_EXPORT_DIR"])
tools = Path(os.environ["LEAPVIEW_TEST_MANAGED_FIXTURE_TOOLS"])
controller = Path(os.environ["LEAPVIEW_TEST_MANAGED_CONTROLLER"])
fixture_binary = tools / "bin/managed-recovery-fixture"
cli = controller / "bin/leapviewctl"
value = fixture.verified_export(private, fixture_binary)
intended = json.loads(Path(artifact_reference).read_bytes())
assert intended["releaseAdmissionQualified"] is False
started = time.monotonic()
state = "/var/lib/coordinator-component"
owner = "recovery-fixture"


def put(machine, path, data, user="root", group="root", mode="0600"):
    # No credentials, provider configuration or full private input in command
    # arguments/driver logs. Temporary host and shared copy are private.
    with tempfile.TemporaryDirectory() as directory:
        source_path = Path(directory) / "fixture-input"
        source_path.write_bytes(data if isinstance(data, bytes) else data.encode())
        source_path.chmod(0o600)
        machine.copy_from_host(str(source_path), path)
    machine.succeed("chown " + user + ":" + group + " " + shlex.quote(path)
                    + "; chmod " + mode + " " + shlex.quote(path))


def document(machine, path, data, user="root"):
    put(machine, path, json.dumps(data, separators=(",", ":")), user, user)


def execute(machine, command, stage, timeout=180, succeeds=True):
    out = state + "/" + stage + ".out"
    err = state + "/" + stage + ".err"
    status, _ = machine.execute("umask 077; " + command + " > " + out + " 2> " + err, timeout=timeout)
    if (status == 0) is not succeeds:
        try:
            machine.copy_from_machine(err, "private/" + machine.name)
        except Exception:
            pass  # Diagnostics must never replace the actual failure.
        raise RuntimeError("installed component step " + stage + " returned " + str(status)
                           + "; raw diagnostics remain private in disposable guest")
    return out


def read(machine, path):
    return machine.succeed("cat " + shlex.quote(path))


def sql(machine, statement, database="postgres"):
    put(machine, state + "/query.sql", statement)
    machine.succeed("install -m 0600 -o postgres -g postgres " + state + "/query.sql /run/coordinator-query.sql")
    out = execute(machine, "sudo -u postgres " + postgres_bin + "/psql -XAt -v ON_ERROR_STOP=1 -d "
                  + shlex.quote(database) + " -f /run/coordinator-query.sql", "sql")
    return read(machine, out).strip()


def owner_command(command):
    return "sudo -u " + owner + " " + shlex.quote(str(cli)) + " host " + command


source.start(allow_reboot=True)
authority.start()
replacement.start(allow_reboot=True)
for machine in (source, authority, replacement):
    machine.wait_for_unit("sshd.service", timeout=300)
    machine.succeed("install -d -m 0700 /root/.ssh; install -d -m 0700 -o " + owner
                    + " -g " + owner + " /home/" + owner)
    if machine is not authority:
        machine.wait_for_unit("growpart.service", timeout=300)
        machine.succeed("test -L /nix/var/nix/profiles/system")
        assert machine.succeed("readlink -f /nix/var/nix/profiles/system").strip() == machine.succeed(
            "readlink -f /run/current-system").strip()
        block_size, blocks = map(int, machine.succeed("stat -f -c '%S %b' /").split())
        assert block_size * blocks > 6 * 1024**3, "installed root filesystem did not grow to the fixture disk"
    machine.copy_from_host(str(private.parent / "public-tools.nar"), state + "/public-tools.nar")
    execute(machine, "nix-store --import < " + state + "/public-tools.nar", "public-tools-import", timeout=180)
    assert read(machine, "/etc/machine-id").strip() != "0" * 32

with subtest("immutable actual-publication export installs only on disposable original"):
    source.succeed("test ! -e /var/lib/postgresql/18")
    source.copy_from_host(str(private / "pgdata"), "/var/lib/postgresql/18")
    # This is operator-owned installation on the disposable original, not a
    # privileged import from the replacement recovery owner's writable tree.
    source.succeed("chown -R postgres:postgres /var/lib/postgresql/18; chmod 0750 /var/lib/postgresql/18")
    put(source, "/var/lib/postgresql/18/postgresql.auto.conf", "", "postgres", "postgres")
    execute(source, "openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj /CN=database.local "
            "-addext subjectAltName=DNS:database.local,IP:127.0.0.1,IP:192.168.1.1,IP:192.168.1.2,IP:192.168.1.3 "
            "-keyout " + state + "/server.key -out " + state + "/server.crt", "tls-create")
    ca = read(source, state + "/server.crt")
    key = read(source, state + "/server.key")
    for machine, tls_root in [(source, "/var/lib/leapview-postgres-tls"),
                              (replacement, "/var/lib/leapview-postgres-tls"),
                              (authority, "/var/lib/coordinator-tls")]:
        put(machine, tls_root + "/server.crt", ca, "postgres", "postgres")
        put(machine, tls_root + "/server.key", key, "postgres", "postgres", "0400")
        put(machine, state + "/ca.crt", ca)
        if machine is not authority:
            put(machine, "/var/lib/leapview-backup-secrets/pgbackrest.conf",
                "# Disposable POSIX repository; no provider authentication is required.\n", "root", "pgbackrest", "0640")
    execute(source, "systemctl start postgresql.service", "source-start", timeout=300)
    source.wait_for_unit("postgresql.service", timeout=300)
    system = sql(source, "SELECT system_identifier::text FROM pg_control_system();")
    assert value["set"]["cluster_points"][0]["cluster_identity"] == "postgres-system-id:" + system
    source_machine = read(source, "/etc/machine-id").strip()
    replacement_machine = read(replacement, "/etc/machine-id").strip()
    assert source_machine != replacement_machine
    replacement.fail("sudo -u " + owner + " test -r /var/lib/postgresql/18/PG_VERSION")
    replacement.succeed("test ! -e /var/lib/postgresql/18")

with subtest("actual installed backup frontier has real source preparation and independent authority"):
    backup_command = "sudo -u postgres " + pgbackrest + " --stanza=default "
    execute(source, backup_command + "stanza-create", "stanza-create")
    execute(source, backup_command + "--start-fast --type=full backup", "backup", timeout=300)
    inventory = json.loads(read(source, execute(source, backup_command + "--output=json info", "backup-info")))
    frontier = {"stanza": "default", "backupSet": inventory[0]["backup"][-1]["label"],
                "targetLsn": sql(source, "SELECT pg_current_wal_insert_lsn();"), "systemId": system,
                "timeline": int(sql(source, "SELECT timeline_id FROM pg_control_checkpoint();"))}
    sql(source, "SELECT pg_switch_wal();")
    execute(source, backup_command + "check", "archive-check")
    # Recovery occurrence stale-after is a whole-second duration. Retention
    # and the later enrollment must share that precision before subtraction.
    expires = datetime.now(timezone.utc).replace(microsecond=0) + timedelta(hours=2)
    prepare = {"config": fixture.source_configuration(value, state + "/ca.crt"), "set": value["set"],
               "frontier": frontier, "expiresAt": expires.isoformat(), "receiptFile": state + "/prepared.json"}
    # Use the same separate private client allowed by the installed database
    # module. Source-local loopback is rejected by production pool policy.
    document(replacement, state + "/prepare.json", prepare)
    execute(replacement, "env LEAPVIEW_TEST_MANAGED_PREPARE_FILE=" + state + "/prepare.json "
            + shlex.quote(str(fixture_binary)) + " -test.run '^TestManagedRecoveryInstalledSourcePreparation$' -test.timeout 2m",
            "source-prepare", timeout=180)
    prepared = json.loads(read(replacement, state + "/prepared.json"))
    retained = prepared["set"]
    assert retained["status"] == "prepared" and not retained.get("published_validation_attempt_id")
    execute(authority, "systemctl start postgresql.service", "authority-start", timeout=300)
    authority_system = sql(authority, "SELECT system_identifier::text FROM pg_control_system();")
    assert authority_system != system
    authority_password = uuid.uuid4().hex
    operator_password = uuid.uuid4().hex
    sql(authority, "ALTER ROLE postgres PASSWORD '" + authority_password + "'; CREATE DATABASE managed_recovery_authority;")
    authority_url = "postgres://postgres:" + authority_password + "@192.168.1.3:5432/managed_recovery_authority?sslmode=verify-full"
    operator_url = "postgres://leapview_recovery_operator:" + operator_password + "@192.168.1.3:5432/managed_recovery_authority?sslmode=verify-full"
    for name, raw in [("bootstrap", authority_url), ("operator", operator_url)]:
        put(source, state + "/" + name + ".url", raw)
    bootstrap = {"urlFile": state + "/bootstrap.url", "rootCaFile": state + "/ca.crt", "role": "postgres", "systemIdentifier": authority_system}
    operator = dict(bootstrap, urlFile=state + "/operator.url", role="leapview_recovery_operator")
    document(source, state + "/authority-init.json", {"schemaVersion": 1, "bootstrap": bootstrap, "operator": operator,
             "originalSystemIdentifier": system, "ownerRole": "leapview_recovery_owner", "receiptFile": state + "/authority-receipt.json"})
    execute(source, shlex.quote(str(cli)) + " host init-managed-recovery-authority --input " + state + "/authority-init.json", "authority-init")
    home = value["config"]["HomeDir"]
    source.succeed("install -d -m 0700 " + shlex.quote(home))
    source_url = fixture.tls_url(value["sourceAdminURL"], "127.0.0.1:5432")
    put(source, state + "/source.url", source_url)
    request = {"instanceHome": home, "recoverySetId": retained["id"], "frontierDigest": retained["frontier_digest"],
               "retentionRootId": prepared["rootId"], "sourceSystemId": system, "authoritySystemId": authority_system,
               "artifactIdentity": intended["artifact"]["image"], "actor": "installed-coordinator-component",
               "plannedAt": datetime.now(timezone.utc).replace(microsecond=0).isoformat(), "expiresAt": (expires - timedelta(minutes=5)).isoformat()}
    document(source, state + "/enrollment.json", {"schemaVersion": 1, "request": request,
             "source": dict(bootstrap, urlFile=state + "/source.url", systemIdentifier=system),
             "authority": operator, "receiptFile": state + "/enrollment-receipt.json"})
    execute(source, shlex.quote(str(cli)) + " host enroll-managed-recovery --input " + state + "/enrollment.json", "source-enroll")
    enrollment = json.loads(read(source, state + "/enrollment-receipt.json"))
    assert enrollment["status"] == "prepared"
    for key, original in request.items():
        if key in ("plannedAt", "expiresAt"):
            assert datetime.fromisoformat(enrollment["request"][key]) == datetime.fromisoformat(original)
        else:
            assert enrollment["request"][key] == original

with subtest("separate recovery owner reaches actual fixed restore seam and retained native readback"):
    put(replacement, "/root/.ssh/id_ed25519", Path(fixture_ssh_key).read_bytes())
    host_key = read(source, "/etc/ssh/ssh_host_ed25519_key.pub").strip()
    put(replacement, "/root/.ssh/known_hosts", "192.168.1.2 " + host_key + "\n")
    execute(replacement, "scp -rq -o StrictHostKeyChecking=yes -o IdentitiesOnly=yes -o IdentityAgent=none "
            "-i /root/.ssh/id_ed25519 root@192.168.1.2:/var/lib/coordinator-repository /var/lib/", "copy-archive")
    replacement.succeed("chown -R postgres:postgres /var/lib/coordinator-repository")
    # Private file archives and retained customer keyring are owned by recovery.
    replacement.copy_from_host(str(private / "repository"), "/home/" + owner + "/repository")
    replacement.copy_from_host(str(private / "restic-password"), "/home/" + owner + "/restic-password")
    replacement.succeed("chown -R " + owner + ":" + owner + " /home/" + owner)
    replacement.succeed("install -d -m 0700 -o " + owner + " -g " + owner + " " + shlex.quote(home))
    keyring_path = value["credentials"]["keyringPath"]
    replacement.succeed("install -d -m 0700 -o " + owner + " -g " + owner + " " + shlex.quote(str(Path(keyring_path).parent)))
    put(replacement, keyring_path, fixture.private_bytes(private / "keyring"), owner, owner)
    private_root = "/home/" + owner
    for suffix in ("evidence", "secrets"):
        replacement.succeed("install -d -m 0700 -o " + owner + " -g " + owner + " " + private_root + "/" + suffix)
    put(replacement, private_root + "/identity", Path(fixture_ssh_key).read_bytes(), owner, owner)
    put(replacement, private_root + "/known-hosts", "192.168.1.2 " + host_key + "\n", owner, owner)
    put(replacement, private_root + "/ca.crt", ca, owner, owner)
    put(replacement, private_root + "/authority.url", operator_url, owner, owner)
    fence = {"targetID": retained["delivery"]["target_id"], "ssh": ssh, "identityFile": private_root + "/identity",
             "knownHostsFile": private_root + "/known-hosts", "primaries": [{"address": "192.168.1.2", "machineID": source_machine,
             "systemIdentifier": system, "clusterIdentity": "postgres-system-id:" + system}]}
    document(replacement, private_root + "/fence.json", fence, owner)
    # Existing fence CLI consumes a libpq URL file; retain strict CA verification.
    put(replacement, private_root + "/fence-authority.url", fixture.tls_url(operator_url, "192.168.1.3:5432", private_root + "/ca.crt"), owner, owner)
    execute(replacement, owner_command("fence-recovery --enrollment-file " + private_root + "/fence.json --control-url-file "
            + private_root + "/fence-authority.url --recovery-set-id " + retained["id"]), "original-fence")
    source.fail("systemctl is-active postgresql.service")
    source.succeed("test -s /var/lib/leapview-recovery/postgresql-fence.json")
    source.reboot()
    source.wait_for_unit("sshd.service", timeout=300)
    source.fail("systemctl is-active postgresql.service")
    source.succeed("test -s /var/lib/leapview-recovery/postgresql-fence.json")
    assert sql(authority, "SELECT system_identifier::text FROM pg_control_system();") == authority_system
    credentials = value["credentials"]
    credentials.update(schemaVersion=1, profile="managed-local-v1", recoverySetId=retained["id"], targetId=retained["delivery"]["target_id"],
                       occurrenceId=enrollment["occurrenceId"], postgresRootCa=ca,
                       controlUrl=fixture.tls_url(credentials["controlUrl"], "192.168.1.1:5432"),
                       duckLakeUrl=fixture.tls_url(credentials["duckLakeUrl"], "192.168.1.1:5432"))
    document(replacement, private_root + "/credentials.json", credentials, owner)
    roots = value["roots"]
    for root in roots:
        root.update(Restic=restic, Repository=private_root + "/repository", PasswordFile=private_root + "/restic-password")
        replacement.succeed("install -d -m 0700 -o " + owner + " -g " + owner + " " + shlex.quote(str(Path(root["Destination"]).parent)))
        replacement.succeed("test ! -e " + shlex.quote(root["Destination"]))
    input_value = {"schemaVersion": 1, "profile": "managed-local-v1", "enrollment": enrollment,
                   "recoverySetId": retained["id"], "occurrenceId": enrollment["occurrenceId"], "instanceHome": home,
                   "artifact": intended["artifact"], "credentialsFile": private_root + "/credentials.json", "runtimeRoles": value["roles"],
                   "postgres": {"provider": "module-owned", "frontier": frontier, "destination": "/var/lib/postgresql/18",
                                "metadataSchema": value["metadataSchema"]},
                   "roots": roots, "closure": value["closure"], "primaryFence": fence,
                   "evidenceRoot": private_root + "/evidence", "secretRoot": private_root + "/secrets",
                   "authority": dict(operator, urlFile=private_root + "/authority.url", rootCaFile=private_root + "/ca.crt"),
                   "validationAttemptId": str(uuid.uuid4()), "validator": "installed-coordinator-component", "publisher": "installed-coordinator-component"}
    # The schema is a captured actual native catalog identity, not derived from
    # a guessed convention. The exporter supplies it explicitly.
    input_value["postgres"]["metadataSchema"] = value["metadataSchema"]
    for mutation, name in [(lambda i: i["primaryFence"]["primaries"][0].update(machineID=replacement_machine), "foreign-machine"),
                           (lambda i: i["enrollment"]["request"].update(frontierDigest="sha256:" + "f" * 64), "foreign-frontier")]:
        invalid = copy.deepcopy(input_value)
        mutation(invalid)
        document(replacement, private_root + "/" + name + ".json", invalid, owner)
        execute(replacement, owner_command("qualify-managed-recovery --input " + private_root + "/" + name
                + ".json --output " + private_root + "/" + name + ".receipt"), name, succeeds=False)
        replacement.succeed("test ! -e " + private_root + "/" + name + ".receipt; test ! -e /var/lib/postgresql/18")
    document(replacement, private_root + "/managed.json", input_value, owner)
    execute(replacement, owner_command("qualify-managed-recovery --input " + private_root + "/managed.json --output "
            + private_root + "/qualification.json"), "qualify", timeout=900)
    receipt = fixture.verify_component_receipt(json.loads(read(replacement, private_root + "/qualification.json")))
    assert receipt["admission"]["recoverySetId"] == retained["id"]
    assert receipt["admission"]["frontierDigest"] == retained["frontier_digest"]
    replacement.fail("systemctl is-active postgresql.service")
    replacement.fail("sudo -u " + owner + " test -r /var/lib/postgresql/18/PG_VERSION")
    # Independent admission after boot verifies completed replay and exact root
    # receipt with the actual replacement machine/module generation retained.
    replacement.reboot()
    replacement.wait_for_unit("sshd.service", timeout=300)
    replacement.fail("systemctl is-active postgresql.service")
    execute(replacement, owner_command("admit-managed-recovery --input " + private_root + "/managed.json --output "
            + private_root + "/after-reboot.json"), "admit-after-reboot", timeout=300)
    admission = json.loads(read(replacement, private_root + "/after-reboot.json"))
    assert admission["frontierDigest"] == retained["frontier_digest"] and admission["activationQualified"] is False
    replacement.fail("systemctl is-active postgresql.service")
    source.fail("systemctl is-active postgresql.service")
    print(json.dumps({"scope": "disposable-installed-coordinator-component", "fixtureBundleSHA256": fixture.private_bytes(private / "bundle.sha256").decode(),
                      "fixtureProducerSHA256": value["producerSHA256"], "fixtureSourceRevision": value["sourceRevision"],
                      "fixtureProducerSourceDirty": value["producerSourceDirty"], "controllerSHA256": fixture.sha256_file(cli),
                      "intendedArtifact": intended["artifact"], "activationQualified": False, "releaseAdmissionQualified": False,
                      "fullManagedProfileQualified": False, "durationSeconds": round(time.monotonic() - started, 3)}, sort_keys=True))
