import importlib.util
import json
import os
from pathlib import Path
import shutil
import socket
import pwd
import subprocess
import tempfile
import unittest
import time

spec = importlib.util.spec_from_file_location("postgres_promote", Path(__file__).parents[1] / "modules/postgres_promote.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class PromotionTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        self.state, self.data = root / "state", root / "data"
        self.state.mkdir(mode=0o750)
        self.data.mkdir(mode=0o700)
        (self.data / "pg_tblspc").mkdir()
        (self.data / "pg_wal").mkdir()
        (self.data / "recovery.signal").write_text("")
        (self.data / "postgresql.auto.conf").write_text("shared_preload_libraries='untrusted'\ninclude='untrusted'\narchive_command='untrusted'\n")
        self.config = root / "module-postgresql.conf"
        self.config.write_text("# reviewed configuration\n")
        self.machine = root / "machine-id"
        self.machine.write_text("b" * 32)
        self.wanted = {"schemaVersion": 1, "targetID": "target", "recoverySetID": "set",
                       "frontierDigest": "sha256:" + "c" * 64, "sourceMachineID": "a" * 32,
                       "replacementMachineID": "b" * 32, "systemIdentifier": "12345", "targetLSN": "0/ABC", "timeline": 1}
        self.wanted["dataDirectoryDigest"] = module.digest("leapview-managed-recovery-data:", str(self.data))
        self.active, self.promoted, self.fail_start, self.fail_promote, self.fail_receipt = False, False, False, False, False
        self.calls, self.override = [], {}
        self.start_mode = 0o700
        self.generation = "/nix/store/reviewed-generation"
        self.promotion = module.Promotion(self.state, self.data, self.machine, root / "generation", self.config,
                                          "/module/bin", "systemctl", "runuser", "/module/pgbackrest", 5432, self.execute)
        self.promotion._module_identity = lambda: self.generation

    def execute(self, *args):
        self.calls.append(args)
        if args[0] == "/module/bin/pg_controldata":
            return "Database system identifier: 12345\nDatabase cluster state: " + ("in production" if self.promoted else "shut down in recovery")
        if args[0] == "systemctl":
            if args[1] == "start":
                self.assertTrue((self.state / module.INTENT).is_file())
                auto = (self.data / "postgresql.auto.conf").read_text()
                self.assertNotIn("untrusted", auto)
                self.assertIn("listen_addresses = '127.0.0.1'", auto)
                if self.fail_start:
                    raise subprocess.CalledProcessError(1, args, stderr="private provider diagnostic")
                self.data.chmod(self.start_mode)
                self.active = True
                return ""
            if args[1] == "stop":
                self.active = False
                return ""
            return "ActiveState=active\nSubState=running\nMainPID=42\n" if self.active else "ActiveState=inactive\nSubState=dead\nMainPID=0\n"
        if "pg_ctl" in str(args):
            intent = json.loads((self.state / module.INTENT).read_text())
            self.assertEqual(intent["request"], self.wanted)
            self.assertFalse((self.state / module.RECEIPT).exists())
            self.promoted = True
            (self.data / "pg_wal/00000002.history").write_text("1\t0/ABC\ttest promotion\n")
            if self.fail_promote:
                raise subprocess.CalledProcessError(1, args, stderr="private provider diagnostic")
            return ""
        if args[-1] == "SELECT pg_catalog.pg_is_in_recovery()":
            return "f" if self.promoted else "t"
        if args[-1] == "CHECKPOINT":
            return "CHECKPOINT"
        value = {"system": "12345", "recovery": not self.promoted, "paused": not self.promoted,
                 "lsn": "0/ABC", "timeline": 2 if self.promoted else 1, "data": str(self.data),
                 "target": "0/ABC", "targetTimeline": "1",
                 "unsafeRoleSettings": False,
                 "config": str(self.config), "listen": "127.0.0.1", "shared": "", "session": "", "local": ""}
        value.update(self.override)
        return json.dumps(value)

    def test_promotion_durable_intent_private_ingress_exact_retry_and_check(self):
        result = self.promotion.apply(self.wanted)
        self.assertTrue(self.promoted)
        self.assertEqual(result["status"], "promoted-loopback-only")
        self.assertFalse(result["activationQualified"])
        self.assertFalse(result["fullManagedProfileQualified"])
        self.assertNotIn("machineID", json.dumps(result))
        self.assertNotIn("private provider", json.dumps(result))
        self.assertEqual((self.state / module.INTENT).stat().st_mode & 0o777, 0o600)
        self.assertEqual((self.state / module.RECEIPT).stat().st_mode & 0o777, 0o640)
        self.calls.clear()
        self.assertEqual(self.promotion.apply(self.wanted), result)
        self.assertEqual(self.promotion.apply(self.wanted, check=True), result)
        self.assertFalse(any("promote" in call for call in self.calls))

    def test_actual_module_private_group_mode_before_and_after_start_and_retry(self):
        self.data.chmod(0o750)
        self.start_mode = 0o750
        receipt = self.promotion.apply(self.wanted)
        self.assertEqual(self.data.stat().st_mode & 0o777, 0o750)
        self.assertEqual(self.promotion.apply(self.wanted, check=True), receipt)
        self.assertEqual(self.promotion.apply(self.wanted), receipt)

    def test_wrong_group_or_nonprivate_mode_rejected_before_service_start(self):
        for mode, group in [(0o770, os.getegid()), (0o755, os.getegid()), (0o750, os.getegid() + 1), (0o700, os.getegid() + 1)]:
            with self.subTest(mode=oct(mode), group=group):
                self.setUp()
                self.data.chmod(mode)
                self.promotion.service_gid = group
                with self.assertRaises(ValueError):
                    self.promotion.apply(self.wanted)
                self.assertFalse(any(call[:2] == ("systemctl", "start") for call in self.calls))
                self.assertFalse((self.state / module.RECEIPT).exists())

    def test_service_start_cannot_broaden_data_permissions_or_group(self):
        for mode, change_group in [(0o770, False), (0o755, False), (0o750, True)]:
            with self.subTest(mode=oct(mode), change_group=change_group):
                self.setUp()
                self.start_mode = mode
                original = self.promotion.execute
                def execute(*args):
                    result = original(*args)
                    if args[:2] == ("systemctl", "start") and change_group:
                        self.promotion.service_gid = os.getegid() + 1
                    return result
                self.promotion.execute = execute
                with self.assertRaises(ValueError):
                    self.promotion.apply(self.wanted)
                self.assertFalse(self.active)
                self.assertFalse(self.promoted)
                self.assertFalse((self.state / module.RECEIPT).exists())

    def test_crash_after_promotion_reconciles_only_authenticated_exact_intent(self):
        self.fail_promote = True
        with self.assertRaises(subprocess.CalledProcessError):
            self.promotion.apply(self.wanted)
        self.assertTrue(self.promoted)
        self.assertFalse(self.active)
        self.assertTrue((self.state / module.INTENT).exists())
        self.assertFalse((self.state / module.RECEIPT).exists())
        self.fail_promote = False
        result = self.promotion.apply(self.wanted)
        self.assertEqual(result["status"], "promoted-loopback-only")
        self.assertEqual(sum("promote" in call for call in self.calls), 1)

    def test_already_promoted_without_authenticated_intent_cannot_qualify(self):
        self.promoted = True
        with self.assertRaises(ValueError):
            self.promotion.apply(self.wanted)
        self.assertFalse((self.state / module.INTENT).exists())
        self.assertFalse((self.state / module.RECEIPT).exists())
        self.assertFalse(any(call[0] == "systemctl" and call[1] in ("start", "stop") for call in self.calls))

    def test_wrong_host_and_foreign_frontier_cannot_mutate_owned_service(self):
        with self.assertRaises(ValueError):
            self.promotion.apply(dict(self.wanted, replacementMachineID="d" * 32))
        self.assertFalse((self.state / module.INTENT).exists())
        self.promotion.apply(self.wanted)
        self.calls.clear()
        for key, value in [("targetID", "other"), ("frontierDigest", "sha256:" + "e" * 64), ("targetLSN", "0/ABD")]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.promotion.apply(dict(self.wanted, **{key: value}))
        self.assertTrue(self.active)
        self.assertFalse(any(call[0] == "systemctl" and call[1] in ("start", "stop") for call in self.calls))

    def test_foreign_or_unsafe_observed_service_is_stopped_without_receipt(self):
        for key, value in [("system", "999"), ("target", "0/ABD"), ("timeline", 3), ("data", "/wrong"),
                           ("listen", "0.0.0.0"), ("shared", "untrusted"), ("session", "untrusted"), ("unsafeRoleSettings", True), ("paused", False)]:
            with self.subTest(key=key):
                if (self.state / module.INTENT).exists():
                    (self.state / module.INTENT).unlink()
                self.active, self.promoted = False, False
                self.override = {key: value}
                with self.assertRaises(ValueError):
                    self.promotion.apply(self.wanted)
                self.assertFalse(self.active)
                self.assertFalse((self.state / module.RECEIPT).exists())

    def test_generation_change_or_replaced_receipt_invalidates_check(self):
        self.promotion.apply(self.wanted)
        self.generation = "/nix/store/other-generation"
        with self.assertRaises(ValueError):
            self.promotion.apply(self.wanted, check=True)
        self.generation = "/nix/store/reviewed-generation"
        receipt = self.state / module.RECEIPT
        receipt.chmod(0o660)
        with self.assertRaises(ValueError):
            self.promotion.apply(self.wanted, check=True)
        receipt.chmod(0o640)
        receipt.rename(self.state / "original")
        receipt.symlink_to(self.state / "original")
        with self.assertRaises(ValueError):
            self.promotion.apply(self.wanted, check=True)

    def test_restored_links_and_check_without_receipt_fail_closed(self):
        with self.assertRaises(ValueError):
            self.promotion.apply(self.wanted, check=True)
        (self.data / "postgresql.auto.conf").unlink()
        (self.data / "postgresql.auto.conf").symlink_to(self.config)
        with self.assertRaises(ValueError):
            self.promotion.apply(self.wanted)
        self.assertFalse((self.state / module.INTENT).exists())

    def test_start_failure_preserves_exact_intent_for_explicit_retry(self):
        self.fail_start = True
        with self.assertRaises(subprocess.CalledProcessError):
            self.promotion.apply(self.wanted)
        self.assertFalse(self.active)
        self.assertTrue((self.state / module.INTENT).exists())
        self.fail_start = False
        self.assertEqual(self.promotion.apply(self.wanted)["status"], "promoted-loopback-only")

    def test_strict_request_rejects_duplicate_extra_and_same_host(self):
        for raw in [json.dumps(dict(self.wanted, extra=True)), json.dumps(dict(self.wanted, schemaVersion=True)),
                    json.dumps(dict(self.wanted, sourceMachineID="b" * 32)), json.dumps(dict(self.wanted, timeline=True)),
                    '{"schemaVersion":1,"schemaVersion":1}', "[]", "{}"]:
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                module.request(raw)


class NativePromotionRestartTest(unittest.TestCase):
    def test_real_paused_frontier_promotion_and_post_promotion_restart(self):
        binary = shutil.which("pg_ctl")
        if binary is None or os.geteuid() == 0:
            if os.environ.get("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED") == "1":
                self.fail("native promotion regression requires unprivileged locked PostgreSQL tools")
            self.skipTest("native unprivileged PostgreSQL tools unavailable")
        binaries = Path(binary).parent
        with tempfile.TemporaryDirectory(prefix="leapview-promotion-native-") as temporary:
            root = Path(temporary)
            os.chmod(root, 0o700)
            source, restored, unix, archive, state = [root / name for name in ("source", "restored", "socket", "archive", "state")]
            for path in (unix, archive):
                path.mkdir(mode=0o700)
            state.mkdir(mode=0o750)
            port_socket = socket.socket()
            port_socket.bind(("127.0.0.1", 0))
            port = port_socket.getsockname()[1]
            port_socket.close()

            def native(*args):
                return subprocess.run([str(arg) for arg in args], check=True, capture_output=True, text=True, timeout=90,
                                      env={"PATH": os.environ.get("PATH", ""), "LC_ALL": "C"}).stdout.strip()

            def sql(statement):
                return native(binaries / "psql", "-XAt", "-h", unix, "-p", port, "-U", "postgres", "-d", "postgres", "-c", statement)

            def start(data, config=None):
                options = "-h 127.0.0.1 -k " + str(unix) + " -p " + str(port)
                if config:
                    # Match the pinned module StateDirectoryMode on each start.
                    data.chmod(0o750)
                    target = data / "postgresql.conf"
                    if target.exists() or target.is_symlink():
                        target.unlink()
                    target.symlink_to(config)
                native(binaries / "pg_ctl", "-D", data, "-l", root / (data.name + ".log"), "-o", options, "-w", "start")

            def stop(data):
                if (data / "postmaster.pid").exists():
                    native(binaries / "pg_ctl", "-D", data, "-m", "fast", "-w", "stop")

            try:
                native(binaries / "initdb", "-D", source, "-U", "postgres", "--no-locale", "--auth=trust")
                with (source / "postgresql.conf").open("a") as output:
                    output.write("\narchive_mode=on\narchive_command='" + "cp %p " + str(archive) + "/%f'\n")
                start(source)
                sql("CREATE TABLE retained_frontier(value integer); INSERT INTO retained_frontier VALUES (1)")
                sql("CREATE ROLE native_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD 'fixture-only-secret'; CREATE ROLE native_maintenance LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD 'fixture-only-maintenance'")
                # CREATE DATABASE cannot run inside the multi-command implicit
                # transaction. Both accounts are ordinary password roles.
                sql("CREATE DATABASE leapview_control")
                sql("CREATE DATABASE leapview_ducklake")
                system = sql("SELECT system_identifier FROM pg_control_system()")
                native(binaries / "pg_basebackup", "-h", unix, "-p", port, "-U", "postgres", "-D", restored,
                       "-X", "stream", "--checkpoint=fast")
                sql("INSERT INTO retained_frontier VALUES (2)")
                target = sql("SELECT pg_current_wal_insert_lsn()")
                sql("SELECT pg_switch_wal()")
                deadline = time.monotonic() + 30
                while not list(archive.glob("00000001*")):
                    if time.monotonic() > deadline:
                        self.fail("native source WAL archive unavailable")
                    time.sleep(0.1)
                stop(source)

                (restored / "recovery.signal").write_text("")
                certificate, key = root / "server.crt", root / "server.key"
                native("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", key,
                       "-out", certificate, "-days", "1", "-subj", "/CN=managed-recovery.example",
                       "-addext", "subjectAltName=DNS:managed-recovery.example")
                key.chmod(0o600)
                managed = Path(__file__).parents[1]
                module_hba = subprocess.run(["nix", "eval", "--no-update-lock-file", "--raw",
                                            ".#nixosConfigurations.example-database.config.services.postgresql.authentication"],
                                           cwd=managed, check=True, capture_output=True, text=True, timeout=120).stdout
                (restored / "pg_hba.conf").write_text(module_hba)
                # Native fixture runs without root/runuser. This one test map
                # preserves real peer authentication under its actual OS user.
                (restored / "pg_ident.conf").write_text("postgres " + pwd.getpwuid(os.geteuid()).pw_name + " postgres\n")
                trusted = root / "trusted-postgresql.conf"
                trusted.write_text("data_directory='" + str(restored) + "'\nhba_file='" + str(restored / "pg_hba.conf") + "'\n"
                                   "ident_file='" + str(restored / "pg_ident.conf") + "'\nssl=on\nssl_cert_file='" + str(certificate) + "'\nssl_key_file='" + str(key) + "'\n")
                # Only the archive provider and service manager are local test
                # adapters. PostgreSQL restore, replay, promotion, control data
                # and clean restart are real locked native processes.
                archive_get = root / "archive-get"
                archive_get.write_text("#!/bin/sh\nwhile [ \"$#\" -gt 2 ]; do shift; done\nexec " + shutil.which("cp") + " " + str(archive) + "/\"$1\" \"$2\"\n")
                archive_get.chmod(0o700)
                machine = root / "machine-id"
                machine.write_text("b" * 32)
                wanted = {"schemaVersion": 1, "targetID": "target", "recoverySetID": "set", "frontierDigest": "sha256:" + "c" * 64,
                          "sourceMachineID": "a" * 32, "replacementMachineID": "b" * 32, "systemIdentifier": system, "targetLSN": target, "timeline": 1}
                wanted["dataDirectoryDigest"] = module.digest("leapview-managed-recovery-data:", str(restored))
                active = False

                def execute(*args):
                    nonlocal active
                    if args[0] == "systemctl":
                        if args[1] == "start":
                            start(restored, trusted)
                            active = True
                            return ""
                        if args[1] == "stop":
                            stop(restored)
                            active = False
                            return ""
                        return "ActiveState=active\nSubState=running\nMainPID=42\n" if active else "ActiveState=inactive\nSubState=dead\nMainPID=0\n"
                    if args[0] == "runuser":
                        actual = list(args[4:])
                        if actual[0].endswith("psql"):
                            actual[actual.index("-d") + 1] = actual[actual.index("-d") + 1].replace("/run/postgresql", str(unix))
                            actual += ["-U", "postgres"]
                        return native(*actual)
                    return native(*args)

                promotion = module.Promotion(state, restored, machine, root / "generation", trusted, binaries,
                                             "systemctl", "runuser", str(archive_get), port, execute)
                promotion._module_identity = lambda: "/nix/store/native-fixture-generation"
                # Model the actual completed confined readback prerequisite:
                # replay the selected target, pause, then stop before adoption.
                (restored / "postgresql.auto.conf").write_text(promotion._auto_config(wanted))
                start(restored, trusted)
                deadline = time.monotonic() + 30
                while sql("SELECT pg_is_wal_replay_paused()") != "t":
                    if time.monotonic() > deadline:
                        self.fail("native restored frontier did not pause")
                    time.sleep(0.1)
                self.assertEqual(sql("SELECT current_setting('recovery_target_lsn')"), target)
                stop(restored)
                real_write = promotion._write
                def crash_before_receipt(path, value, mode):
                    if path.name == module.RECEIPT:
                        raise OSError("simulated controller crash after real promotion")
                    return real_write(path, value, mode)
                promotion._write = crash_before_receipt
                with self.assertRaises(OSError):
                    promotion.apply(wanted)
                self.assertTrue((state / module.INTENT).is_file())
                self.assertFalse((state / module.RECEIPT).exists())
                self.assertFalse((restored / "postmaster.pid").exists())
                promotion._write = real_write
                receipt = promotion.apply(wanted)
                self.assertEqual(sql("SELECT sum(value) FROM retained_frontier"), "3")
                for role, password in [("native_runtime", "fixture-only-secret"), ("native_maintenance", "fixture-only-maintenance")]:
                    connection = "host=managed-recovery.example hostaddr=127.0.0.1 port=" + str(port) + " dbname=leapview_control user=" + role + " password=" + password + " sslmode=verify-full sslrootcert=" + str(certificate)
                    actual = native(binaries / "psql", "-XAt", "-d", connection, "-c",
                                    "SELECT current_user,ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()")
                    self.assertEqual(actual, role + "|t")
                    rejected = subprocess.run([str(binaries / "psql"), "-XAt", "-d", connection.replace("sslmode=verify-full", "sslmode=disable"), "-c", "SELECT 1"], capture_output=True, text=True, timeout=10)
                    self.assertNotEqual(rejected.returncode, 0, "actual module HBA must reject plaintext adoption")
                self.assertEqual(sql("SELECT current_setting('config_file')"), str(restored / "postgresql.conf"))
                stop(restored)
                active = False
                self.assertEqual(promotion.apply(wanted), receipt)
                self.assertEqual(sql("SELECT sum(value) FROM retained_frontier"), "3")
            finally:
                stop(restored)
                stop(source)


class UnprivilegedReceiptAccessTest(unittest.TestCase):
    def test_dedicated_receipt_group_does_not_grant_postgres_data_access(self):
        if shutil.which("docker") is None:
            if os.environ.get("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED") == "1":
                self.fail("required unprivileged receipt access regression needs Docker")
            self.skipTest("isolated UID/GID access fixture requires Docker")
        # Evaluate the actual module with a separately provisioned owner. This
        # must fail if membership, receipt ownership/mode or sudo wiring drifts;
        # a hand-authored correct Unix fixture alone would miss that regression.
        expression = '''
let
  flake = builtins.getFlake (builtins.getEnv "LEAPVIEW_TEST_MANAGED_MODULE");
  host = flake.nixosConfigurations.example-database.extendModules {
    modules = [{
      leapview.database.recoveryOwner = "recovery-fixture";
      users.users.recovery-fixture = { isSystemUser = true; group = "recovery-fixture"; };
      users.groups.recovery-fixture = {};
    }];
  };
in {
  groups = host.config.users.users.recovery-fixture.extraGroups;
  directories = builtins.filter (rule: builtins.match "d /var/lib/leapview-recovery-adoption .*" rule != null) host.config.systemd.tmpfiles.rules;
  sudo = builtins.filter (rule: builtins.elem "recovery-fixture" rule.users) host.config.security.sudo.extraRules;
}
'''
        evaluated = subprocess.run(["nix", "eval", "--no-update-lock-file", "--impure", "--json", "--expr", expression],
                                   env={**os.environ, "LEAPVIEW_TEST_MANAGED_MODULE": str(Path(__file__).parents[1].resolve())},
                                   check=True, capture_output=True, text=True, timeout=120)
        actual = json.loads(evaluated.stdout)
        self.assertEqual(actual["groups"], ["leapview-recovery-adoption"])
        self.assertEqual(len(actual["directories"]), 1)
        rule = actual["directories"][0].split()
        self.assertEqual(rule[:5], ["d", "/var/lib/leapview-recovery-adoption", "0750", "root", actual["groups"][0]])
        self.assertEqual(len(actual["sudo"]), 1)
        self.assertEqual(actual["sudo"][0]["commands"], [{"command": "/run/current-system/sw/bin/leapview-postgres-promote", "options": ["NOPASSWD"]}])
        image = "public.ecr.aws/docker/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8"
        script = """
set -eu
mkdir /proof/receipts /proof/data
printf '%s\\n' 'root:x:0:0:root:/root:/bin/sh' > /etc/passwd
printf '%s\\n' 'root:x:0:' > /etc/group
addgroup -g 57432 "$LEAPVIEW_TEST_RECEIPT_GROUP"
chown 0:57432 /proof/receipts
chmod "$LEAPVIEW_TEST_RECEIPT_MODE" /proof/receipts
printf '%s\\n' '{"kind":"leapview/managed-postgresql-promotion"}' > /proof/receipts/promotion.json
chown 0:57432 /proof/receipts/promotion.json
chmod 0640 /proof/receipts/promotion.json
printf '%s\\n' 'private PostgreSQL config' > /proof/data/postgresql.conf
chown 999:999 /proof/data
chmod 0700 /proof/data
drop=$(command -v gosu || command -v su-exec)
"$drop" 57431:57432 sh -ec 'cat /proof/receipts/promotion.json >/dev/null; test ! -r /proof/data/postgresql.conf; test ! -w /proof/receipts/promotion.json'
if "$drop" 57431:57433 sh -ec 'cat /proof/receipts/promotion.json >/dev/null' 2>/dev/null; then exit 1; fi
"""
        result = subprocess.run(["docker", "run", "--rm", "--network=none", "--read-only", "--tmpfs", "/proof:rw,nodev,nosuid,size=1m",
                                 "--tmpfs", "/etc:rw,nodev,nosuid,size=1m", "--env", "LEAPVIEW_TEST_RECEIPT_GROUP=" + rule[4], "--env", "LEAPVIEW_TEST_RECEIPT_MODE=" + rule[2],
                                 "--entrypoint", "/bin/sh", image, "-ec", script], capture_output=True, text=True, timeout=120)
        self.assertEqual(result.returncode, 0, "dedicated receipt-read group must be readable without PostgreSQL group or data-directory access")


if __name__ == "__main__":
    unittest.main()
