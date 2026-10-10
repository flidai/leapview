import importlib.util
import fcntl
import json
import os
from pathlib import Path
import shlex
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import unittest

modules = Path(__file__).parents[1] / "modules"
sys.path.insert(0, str(modules))
spec = importlib.util.spec_from_file_location("postgres_restore", modules / "postgres_restore.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ModuleRestoreTest(unittest.TestCase):
    def setUp(self):
        # Production forbids writable ancestors; /tmp is deliberately rejected.
        temporary = tempfile.TemporaryDirectory(dir=Path(__file__).parents[4])
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.state, self.data = self.root / "state", self.root / "postgres" / "data"
        self.state.mkdir(mode=0o750)
        self.data.parent.mkdir(mode=0o700)
        self.config = self.root / "postgresql.conf"
        self.config.write_text("# reviewed module config\n")
        self.provider = self.root / "pgbackrest.conf"
        self.provider.write_text("[default]\npg1-path=" + str(self.data) + "\n[global]\nrepo1-type=s3\n")
        self.credentials = self.root / "credentials.conf"
        self.credentials.write_text("[global]\nrepo1-cipher-pass=fixture-private-key\n")
        self.credentials.chmod(0o640)
        self.machine = self.root / "machine-id"
        self.machine.write_text("b" * 32)
        self.wanted = {"schemaVersion": 1, "targetID": "target", "recoverySetID": "set",
                       "frontierDigest": "sha256:" + "c" * 64, "sourceMachineID": "a" * 32,
                       "replacementMachineID": "b" * 32, "systemIdentifier": "12345", "targetLSN": "0/ABC", "timeline": 1,
                       "backupSet": "20261010-000000F", "occurrenceID": "occurrence", "operationID": "provider-restore:occurrence"}
        self.wanted["dataDirectoryDigest"] = module.digest("leapview-managed-recovery-data:", str(self.data))
        self.calls, self.active, self.fail_provider, self.fail_receipt, self.override = [], False, False, False, {}
        self.start_mode = 0o700
        self.restorer = module.ModuleRestore(self.state, self.data, self.machine, self.root / "generation", self.config,
                                            "/module/bin", "systemctl", "runuser", "/module/pgbackrest", 5432,
                                            self.provider, self.credentials, execute=self.execute, service_uid=os.geteuid(), service_gid=os.getegid())
        self.restorer._module_identity = lambda: "/nix/store/reviewed-generation"
        self.restorer._provider_binding = lambda: module.digest("fixture-provider:", self.provider.read_text() + self.credentials.read_text())
        self.restorer._provider = lambda arguments, lock: self.execute(*arguments)

    def execute(self, *args):
        self.calls.append(args)
        if "restore" in args:
            stage = Path(next(arg.split("=", 1)[1] for arg in args if arg.startswith("--pg1-path=")))
            (stage / "PG_VERSION").write_text("18\n")
            (stage / "pg_tblspc").mkdir(exist_ok=True)
            (stage / "pg_wal").mkdir(exist_ok=True)
            (stage / "recovery.signal").write_text("")
            (stage / "postgresql.auto.conf").write_text("include='/tmp/untrusted'\n")
            if self.fail_provider:
                raise subprocess.CalledProcessError(1, args, stderr="private provider failure")
            return ""
        if args[0].endswith("pg_controldata"):
            return "Database system identifier: 12345\nDatabase cluster state: shut down in recovery\n"
        if args[0] == "systemctl":
            if args[1] == "start":
                self.assertTrue((self.state / module.INTENT).is_file())
                self.assertNotIn("untrusted", (self.data / "postgresql.auto.conf").read_text())
                self.data.chmod(self.start_mode)
                self.active = True
                return ""
            if args[1] == "stop":
                self.active = False
                return ""
            return "ActiveState=active\nSubState=running\nMainPID=42\n" if self.active else "ActiveState=inactive\nSubState=dead\nMainPID=0\n"
        value = {"system": "12345", "recovery": True, "paused": True, "lsn": "0/ABC", "timeline": 1,
                 "target": "0/ABC", "targetTimeline": "1", "data": str(self.data), "config": str(self.config),
                 "listen": "127.0.0.1", "shared": "", "session": "", "local": "", "unsafeRoleSettings": False}
        value.update(self.override)
        return json.dumps(value)

    def test_fixed_provider_restore_stops_and_replays_without_second_restore(self):
        result = self.restorer.apply(self.wanted)
        self.assertFalse(self.active)
        self.assertEqual(result["status"], "restored-stopped")
        self.assertFalse(result["activationQualified"])
        self.assertFalse(result["fullManagedProfileQualified"])
        self.assertNotIn("fixture-private-key", json.dumps(result))
        self.assertNotIn("b" * 32, json.dumps(result))
        self.assertEqual(self.restorer.apply(self.wanted), result)
        self.assertEqual(self.restorer.apply(self.wanted, action="check"), result)
        self.assertEqual(sum("restore" in call for call in self.calls), 1)
        self.restorer.apply(self.wanted, action="start-readback")
        self.assertTrue(self.active)
        self.restorer.apply(self.wanted, action="stop-readback")
        self.assertFalse(self.active)

    def test_actual_module_group_mode_survives_restore_readback_and_completed_retry(self):
        self.start_mode = 0o750
        receipt = self.restorer.apply(self.wanted)
        self.assertEqual(self.data.stat().st_mode & 0o777, 0o750)
        self.assertEqual(self.restorer.apply(self.wanted), receipt)
        self.assertEqual(self.restorer.apply(self.wanted, action="check"), receipt)
        self.restorer.apply(self.wanted, action="start-readback")
        self.restorer.apply(self.wanted, action="stop-readback")
        self.assertFalse(self.active)
        self.assertEqual(sum("restore" in call for call in self.calls), 1)

    def test_service_start_permission_drift_stops_without_restore_receipt(self):
        self.start_mode = 0o770
        with self.assertRaises(ValueError):
            self.restorer.apply(self.wanted)
        self.assertFalse(self.active)
        self.assertFalse((self.state / module.RECEIPT).exists())

    def test_unrelated_existing_data_or_service_never_mutated(self):
        self.data.mkdir(mode=0o700)
        (self.data / "unrelated").write_text("keep")
        with self.assertRaises(ValueError):
            self.restorer.apply(self.wanted)
        self.assertEqual((self.data / "unrelated").read_text(), "keep")
        self.assertFalse(any("restore" in call or "stop" in call for call in self.calls))
        self.data.joinpath("unrelated").unlink()
        self.data.rmdir()
        self.active = True
        with self.assertRaises(ValueError):
            self.restorer.apply(self.wanted)
        self.assertTrue(self.active)

    def test_missing_module_parent_created_only_for_fresh_stopped_restore(self):
        self.data.parent.rmdir()
        self.active = True
        with self.assertRaises(ValueError):
            self.restorer.apply(self.wanted)
        self.assertFalse(self.data.parent.exists())
        self.active = False
        self.restorer.apply(self.wanted)
        self.assertEqual(self.data.parent.stat().st_mode & 0o777, 0o700)

    def test_readonly_fresh_action_never_creates_or_accepts_owned_state(self):
        self.data.parent.rmdir()
        self.assertEqual(self.restorer.apply(self.wanted, action="fresh")["status"], "absent-stopped")
        self.assertFalse(self.data.parent.exists())
        self.assertFalse((self.state / module.INTENT).exists())
        (self.state / module.INTENT).write_text("unrelated retained intent")
        with self.assertRaises(ValueError):
            self.restorer.apply(self.wanted, action="fresh")
        self.assertFalse(self.data.parent.exists())

    def test_generation_or_credentials_change_during_provider_or_start_fails_closed(self):
        for phase in ("provider", "start"):
            for changed in ("generation", "credentials"):
                with self.subTest(phase=phase, changed=changed):
                    self.setUp()
                    original_execute = self.restorer.execute
                    def execute(*args):
                        result = original_execute(*args)
                        if (phase == "provider" and "restore" in args) or (phase == "start" and args[:2] == ("systemctl", "start")):
                            if changed == "generation":
                                self.restorer._module_identity = lambda: "/nix/store/changed-generation"
                            else:
                                self.credentials.write_text("changed retained provider credential\n")
                        return result
                    self.restorer.execute = execute
                    self.restorer._provider = lambda arguments, lock: execute(*arguments)
                    with self.assertRaisesRegex(ValueError, "changed during restore"):
                        self.restorer.apply(self.wanted)
                    self.assertFalse(self.active)
                    self.assertFalse((self.state / module.RECEIPT).exists())
                    if phase == "provider":
                        self.assertFalse(self.data.exists())
                        self.assertFalse(any(call[:2] == ("systemctl", "start") for call in self.calls))

    def test_wrong_host_path_schema_or_operation_cannot_restore(self):
        for key, value in [("replacementMachineID", "d" * 32), ("dataDirectoryDigest", "sha256:" + "e" * 64),
                           ("schemaVersion", True), ("backupSet", "../foreign"), ("extra", True)]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.restorer.apply(dict(self.wanted, **{key: value}))
        self.assertFalse(any("restore" in call for call in self.calls))

    def test_foreign_frontier_or_provider_rotation_cannot_reuse_intent(self):
        self.fail_provider = True
        with self.assertRaises(subprocess.CalledProcessError):
            self.restorer.apply(self.wanted)
        for key, value in [("targetLSN", "0/ABD"), ("operationID", "other"), ("frontierDigest", "sha256:" + "e" * 64)]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.restorer.apply(dict(self.wanted, **{key: value}))
        self.credentials.write_text("changed")
        with self.assertRaises(ValueError):
            self.restorer.apply(self.wanted)

    def test_missing_receipt_cannot_check_or_start_and_unsafe_replay_never_receipted(self):
        for action in ("check", "start-readback", "stop-readback"):
            with self.subTest(action=action), self.assertRaises(ValueError):
                self.restorer.apply(self.wanted, action=action)
        self.override = {"paused": False}
        with self.assertRaises(ValueError):
            self.restorer.apply(self.wanted)
        self.assertFalse(self.active)
        self.assertFalse((self.state / module.RECEIPT).exists())

    def test_paused_before_target_cannot_publish_root_receipt(self):
        self.override["lsn"] = "0/1"
        with self.assertRaises(ValueError):
            self.restorer.apply(self.wanted)
        self.assertFalse(self.active)
        self.assertFalse((self.state / module.RECEIPT).exists())

    def test_receipt_write_interruption_resumes_exact_root_controlled_data(self):
        write = self.restorer._write
        def crash(path, value, mode):
            if path.name == module.RECEIPT:
                raise OSError("fixture interruption before receipt")
            return write(path, value, mode)
        self.restorer._write = crash
        with self.assertRaises(OSError):
            self.restorer.apply(self.wanted)
        self.assertFalse(self.active)
        self.assertTrue(self.data.is_dir())
        self.restorer._write = write
        self.assertEqual(self.restorer.apply(self.wanted)["status"], "restored-stopped")
        self.assertEqual(sum("restore" in call for call in self.calls), 1)

    def test_provider_survives_controller_death_holding_shared_promotion_lock(self):
        self._provider_controller_death(process_group=False)

    def test_provider_survives_controller_process_group_death_holding_lock(self):
        self._provider_controller_death(process_group=True)

    def _provider_controller_death(self, process_group):
        lock_path = self.state / "lock"
        lock = os.open(lock_path, os.O_CREAT | os.O_WRONLY, 0o600)
        ready = self.root / "provider-ready"
        finished = self.root / "provider-finished"
        controller = os.fork()
        if controller == 0:
            try:
                if process_group:
                    os.setsid()
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                module.ModuleRestore._provider(self.restorer, [sys.executable, "-c",
                    "import pathlib,time; pathlib.Path(" + repr(str(ready)) + ").touch(); time.sleep(2); "
                    "pathlib.Path(" + repr(str(finished)) + ").touch()"], lock)
                os._exit(0)
            except BaseException:
                os._exit(1)
        os.close(lock)
        try:
            deadline = time.monotonic() + 5
            while not ready.exists():
                if time.monotonic() > deadline:
                    self.fail("supervised provider did not start")
                time.sleep(0.01)
            if process_group:
                os.killpg(controller, signal.SIGTERM)
            else:
                os.kill(controller, signal.SIGKILL)
            os.waitpid(controller, 0)
            controller = None
            retry = os.open(lock_path, os.O_WRONLY)
            try:
                with self.assertRaises(BlockingIOError):
                    fcntl.flock(retry, fcntl.LOCK_EX | fcntl.LOCK_NB)
                deadline = time.monotonic() + 5
                while True:
                    try:
                        fcntl.flock(retry, fcntl.LOCK_EX | fcntl.LOCK_NB)
                        self.assertTrue(finished.exists(), "restore lock released while provider is still writing")
                        break
                    except BlockingIOError:
                        if time.monotonic() > deadline:
                            self.fail("finished provider retained restore lock")
                        time.sleep(0.01)
            finally:
                os.close(retry)
        finally:
            if controller is not None:
                os.kill(controller, signal.SIGKILL)
                os.waitpid(controller, 0)


if __name__ == "__main__":
    unittest.main()


class NativeModuleRestoreTest(unittest.TestCase):
    def test_real_pgbackrest_restore_replay_stopped_retry_and_readback(self):
        configured_bin = os.environ.get("LEAPVIEW_TEST_MANAGED_POSTGRES_BIN")
        configured_provider = os.environ.get("LEAPVIEW_TEST_MANAGED_PGBACKREST")
        binary = str(Path(configured_bin) / "pg_ctl") if configured_bin else shutil.which("pg_ctl")
        provider = configured_provider or shutil.which("pgbackrest")
        if (configured_bin and not Path(binary).is_file()) or (configured_provider and not Path(provider).is_file()):
            self.fail("explicit native module restore tools are unavailable")
        if binary is None or provider is None or os.geteuid() == 0:
            if os.environ.get("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED") in ("1", "true"):
                self.fail("native module restore requires unprivileged locked PostgreSQL and pgBackRest tools")
            self.skipTest("native module restore tools unavailable")
        binaries = Path(binary).parent
        with tempfile.TemporaryDirectory(prefix="module-restore-native-", dir=Path(__file__).parents[4]) as temporary, \
                tempfile.TemporaryDirectory(prefix="mrsocket-") as socket_directory:
            root = Path(temporary)
            source, data, unix, repository, state = [root / name for name in ("source", "restored", "socket", "repository", "state")]
            unix = Path(socket_directory)
            repository.mkdir(mode=0o700)
            state.mkdir(mode=0o750)
            with socket.socket() as port_socket:
                port_socket.bind(("127.0.0.1", 0))
                port = port_socket.getsockname()[1]
            provider_config = root / "provider.conf"
            credentials = root / "secrets" / "credentials.conf"
            credentials.parent.mkdir(mode=0o700)
            credentials.write_text("[global]\n")
            credentials.chmod(0o640)
            provider_config.write_text(f"[global]\nrepo1-type=posix\nrepo1-path={repository}\nlock-path={root / 'locks'}\n"
                                       f"log-level-console=off\nlog-level-file=off\nstart-fast=y\nprocess-max=1\n"
                                       f"[default]\npg1-path={source}\npg1-port={port}\npg1-socket-path={unix}\npg1-user=postgres\n")

            def native(*args):
                return subprocess.run([str(arg) for arg in args], check=True, capture_output=True, text=True, timeout=90,
                                      env={"PATH": os.environ.get("PATH", ""), "LC_ALL": "C"}).stdout.strip()

            def sql(statement):
                return native(binaries / "psql", "-XAt", "-h", unix, "-p", port, "-U", "postgres", "-d", "postgres", "-c", statement)

            def start(directory, config=None):
                if config:
                    # Match module-owned StateDirectoryMode without changing provider execution.
                    directory.chmod(0o750)
                    (directory / "postgresql.conf").unlink(missing_ok=True)
                    (directory / "postgresql.conf").symlink_to(config)
                native(binaries / "pg_ctl", "-D", directory, "-l", root / (directory.name + ".log"),
                       "-o", f"-h 127.0.0.1 -k {unix} -p {port}", "-w", "start")

            def stop(directory):
                if (directory / "postmaster.pid").exists():
                    native(binaries / "pg_ctl", "-D", directory, "-m", "fast", "-w", "stop")

            try:
                native(binaries / "initdb", "-D", source, "-U", "postgres", "--no-locale", "--auth=trust")
                archive = shlex.join([provider, "--config=" + str(provider_config), "--stanza=default", "archive-push"]) + " %p"
                with (source / "postgresql.conf").open("a") as output:
                    output.write("\narchive_mode=on\narchive_command='" + archive.replace("'", "''") + "'\n")
                start(source)
                sql("CREATE TABLE retained_frontier(value integer); INSERT INTO retained_frontier VALUES (1)")
                system = sql("SELECT system_identifier FROM pg_control_system()")
                native(provider, "--config=" + str(provider_config), "--stanza=default", "stanza-create")
                native(provider, "--config=" + str(provider_config), "--stanza=default", "--type=full", "backup")
                info = json.loads(native(provider, "--config=" + str(provider_config), "--stanza=default", "--output=json", "info"))
                backup = info[0]["backup"][0]["label"]
                sql("INSERT INTO retained_frontier VALUES (2)")
                target = sql("SELECT pg_current_wal_insert_lsn()")
                sql("SELECT pg_switch_wal()")
                native(provider, "--config=" + str(provider_config), "--stanza=default", "check")
                stop(source)
                config = root / "reviewed-postgresql.conf"
                config.write_text(f"listen_addresses='127.0.0.1'\nport={port}\nunix_socket_directories='{unix}'\n")
                machine = root / "machine-id"
                machine.write_text("b" * 32)
                wanted = {"schemaVersion": 1, "targetID": "native-target", "recoverySetID": "native-set",
                          "frontierDigest": "sha256:" + "c" * 64, "sourceMachineID": "a" * 32, "replacementMachineID": "b" * 32,
                          "systemIdentifier": system, "targetLSN": target, "timeline": 1,
                          "dataDirectoryDigest": module.digest("leapview-managed-recovery-data:", str(data)),
                          "backupSet": backup, "occurrenceID": "native-occurrence", "operationID": "provider-restore:native-occurrence"}
                calls = []

                def execute(*args):
                    if args[0] == "fixture-systemctl":
                        if args[1] == "start":
                            start(data, config)
                        elif args[1] == "stop":
                            stop(data)
                        else:
                            return "ActiveState=active\nSubState=running\nMainPID=42\n" if (data / "postmaster.pid").exists() else "ActiveState=inactive\nSubState=dead\nMainPID=0\n"
                        return ""
                    if args[0] == "fixture-runuser":
                        args = list(args[4:])
                        args[args.index("-d") + 1] = args[args.index("-d") + 1].replace("/run/postgresql", str(unix))
                        args += ["-U", "postgres"]
                    return native(*args)

                restorer = module.ModuleRestore(state, data, machine, root / "generation", config, binaries,
                                                "fixture-systemctl", "fixture-runuser", provider, port,
                                                provider_config, credentials, execute=execute)
                # Only the disposable fixture's root ownership/module lookup and
                # systemd/runuser interfaces differ; backup, provider supervision,
                # WAL replay, native identity/readback and shutdown are real.
                restorer._module_identity = lambda: "/nix/store/native-fixture-generation"
                restorer._provider_binding = lambda: module.digest("native-provider:", provider_config.read_text())
                actual_provider = restorer._provider

                def restore(arguments, lock):
                    calls.append(arguments)
                    actual_provider(arguments[4:], lock)

                restorer._provider = restore
                first = restorer.apply(wanted)
                self.assertFalse((data / "postmaster.pid").exists())
                self.assertEqual(restorer.apply(wanted), first)
                self.assertEqual(len(calls), 1)
                restorer.apply(wanted, action="start-readback")
                self.assertEqual(sql("SELECT string_agg(value::text, ',' ORDER BY value) FROM retained_frontier"), "1,2")
                self.assertEqual(sql("SELECT pg_is_in_recovery() AND pg_is_wal_replay_paused()"), "t")
                restorer.apply(wanted, action="stop-readback")
                self.assertEqual(restorer.apply(wanted, action="check"), first)
                self.assertFalse(first["activationQualified"])
                self.assertFalse(first["fullManagedProfileQualified"])
            finally:
                stop(source)
                stop(data)
