"""Native PostgreSQL regressions; supply the locked package with POSTGRES_BIN."""

import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("bootstrap.py")
PASSWORD_FILES = (
    "control-runtime-password", "control-migrator-password",
    "control-maintenance-password", "control-upgrade-coordinator-password",
    "ducklake-runtime-password", "ducklake-migrator-password",
    "ducklake-maintenance-password",
)


class NativeBootstrapTest(unittest.TestCase):
    def setUp(self):
        self.bin = Path(os.environ["POSTGRES_BIN"])
        self.directory = tempfile.TemporaryDirectory(prefix="lv-native-pg-")
        self.root = Path(self.directory.name)
        self.data, self.socket, self.credentials = (self.root / name for name in ("data", "socket", "credentials"))
        self.socket.mkdir(mode=0o700)
        self.credentials.mkdir(mode=0o700)
        self.passwords = {name: secrets.token_urlsafe(32) for name in PASSWORD_FILES}
        for name, password in self.passwords.items():
            path = self.credentials / name
            path.write_text(password + "\n")
            path.chmod(0o600)
        subprocess.run([str(self.bin / "initdb"), "-D", str(self.data), "-U", "postgres", "--auth=reject"],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        # Exercise real peer authentication to postgres without creating a
        # machine-wide OS user. The mapped identity is this disposable runner.
        import pwd
        operator = pwd.getpwuid(os.getuid()).pw_name
        (self.data / "pg_ident.conf").write_text("bootstrap " + operator + " postgres\n")
        (self.data / "pg_hba.conf").write_text("local all postgres peer map=bootstrap\nlocal all all scram-sha-256\n")
        self.hba = (self.data / "pg_hba.conf").read_bytes()
        subprocess.run([str(self.bin / "pg_ctl"), "-D", str(self.data), "-l", str(self.root / "server.log"),
                        "-o", "-k " + str(self.socket) + " -p 55437 -c listen_addresses=''", "-w", "start"],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        self.system_id = self.sql("SELECT system_identifier FROM pg_control_system()")

    def tearDown(self):
        subprocess.run([str(self.bin / "pg_ctl"), "-D", str(self.data), "-m", "immediate", "-w", "stop"],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        self.directory.cleanup()

    def sql(self, query, database="postgres", role="postgres", succeeds=True):
        env = {key: value for key, value in os.environ.items() if not key.startswith("PG")}
        if role != "postgres":
            password = self.passwords[role.removeprefix("leapview_").replace("_", "-") + "-password"]
            password_file = self.root / "pgpass"
            password_file.write_text("*:*:*:*:" + password + "\n")
            password_file.chmod(0o600)
            env["PGPASSFILE"] = str(password_file)
        result = subprocess.run([str(self.bin / "psql"), "-X", "-w", "-h", str(self.socket), "-p", "55437",
                                 "-U", role, "-d", database, "-At", "-v", "ON_ERROR_STOP=1"],
                                input=query, text=True, capture_output=True, env=env)
        self.assertEqual(result.returncode == 0, succeeds, result.stderr)
        return result.stdout.strip()

    def bootstrap(self, succeeds=True, system_id=None):
        result = subprocess.run([os.environ.get("PYTHON", "python3"), str(SCRIPT),
                                 "--psql", str(self.bin / "psql"), "--socket", str(self.socket), "--port", "55437",
                                 "--credentials", str(self.credentials), "--system-identifier", system_id or self.system_id],
                                text=True, capture_output=True)
        self.assertEqual(result.returncode == 0, succeeds, result.stderr)
        for password in self.passwords.values():
            self.assertNotIn(password, result.stdout + result.stderr)
        self.assertEqual((self.data / "pg_hba.conf").read_bytes(), self.hba)
        return result

    def test_real_peer_bootstrap_and_role_boundaries(self):
        report = json.loads(self.bootstrap().stdout)
        self.assertEqual(report["status"], "provisioned")
        self.assertEqual(report["systemIdentifier"], self.system_id)
        self.assertEqual(self.sql("SELECT count(*) FROM pg_roles WHERE rolname LIKE 'leapview_%'"), "11")
        self.assertEqual(self.sql("SELECT count(*) FROM pg_roles WHERE rolname LIKE 'leapview_%' AND "
                                 "(rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR rolinherit)"), "0")
        for database, prefix in (("leapview_control", "control"), ("leapview_ducklake", "ducklake")):
            for kind in ("runtime", "maintenance"):
                role = "leapview_" + prefix + "_" + kind
                self.assertEqual(self.sql("SELECT current_user", database, role), role)
                self.sql("SET ROLE leapview_" + prefix + "_owner", database, role, succeeds=False)
                self.sql("CREATE SCHEMA unauthorized", database, role, succeeds=False)
                self.sql("CREATE TABLE " + ("ducklake" if prefix == "ducklake" else "public") + ".unauthorized(id int)",
                         database, role, succeeds=False)
                other = "leapview_ducklake" if prefix == "control" else "leapview_control"
                self.sql("SELECT 1", other, role, succeeds=False)
            migrator = "leapview_" + prefix + "_migrator"
            schema = "public" if prefix == "control" else "ducklake"
            self.sql("SET ROLE leapview_" + prefix + "_owner; CREATE TABLE " + schema + ".migration_probe(id int GENERATED BY DEFAULT AS IDENTITY)",
                     database, migrator)
        coordinator = "leapview_control_upgrade_coordinator"
        self.assertEqual(self.sql("SELECT current_user", "leapview_control", coordinator), coordinator)
        self.sql("SET ROLE leapview_control_owner", "leapview_control", coordinator, succeeds=False)
        self.sql("CREATE TABLE public.unauthorized(id int)", "leapview_control", coordinator, succeeds=False)
        self.sql("SELECT 1", "leapview_ducklake", coordinator, succeeds=False)
        self.sql("INSERT INTO ducklake.migration_probe VALUES (42)", "leapview_ducklake", "leapview_ducklake_runtime")
        self.assertEqual(self.sql("SELECT id FROM ducklake.migration_probe", "leapview_ducklake", "leapview_ducklake_maintenance"), "42")
        self.assertEqual(self.sql("INSERT INTO ducklake.migration_probe DEFAULT VALUES RETURNING id",
                                 "leapview_ducklake", "leapview_ducklake_runtime"), "1\nINSERT 0 1")

    def test_repeat_refuses_without_changing_credentials_or_data(self):
        self.bootstrap()
        self.sql("CREATE TABLE public.retained(id int); INSERT INTO public.retained VALUES (42)", "leapview_control")
        before = self.sql("SELECT rolname || ':' || coalesce(rolpassword,'') FROM pg_authid ORDER BY rolname")
        self.bootstrap(succeeds=False)
        self.assertEqual(self.sql("SELECT rolname || ':' || coalesce(rolpassword,'') FROM pg_authid ORDER BY rolname"), before)
        self.assertEqual(self.sql("SELECT id FROM public.retained", "leapview_control"), "42")

    def test_foreign_existing_role_or_database_is_not_modified(self):
        self.sql("CREATE ROLE leapview_control_runtime SUPERUSER; CREATE DATABASE foreign_customer")
        self.bootstrap(succeeds=False)
        self.assertEqual(self.sql("SELECT rolsuper FROM pg_roles WHERE rolname='leapview_control_runtime'"), "t")
        self.assertEqual(self.sql("SELECT count(*) FROM pg_database WHERE datname='leapview_control'"), "0")

    def test_existing_application_objects_are_not_adopted(self):
        self.sql("CREATE TABLE public.foreign_state(id int); INSERT INTO public.foreign_state VALUES (9)")
        self.bootstrap(succeeds=False)
        self.assertEqual(self.sql("SELECT id FROM public.foreign_state"), "9")
        self.assertEqual(self.sql("SELECT count(*) FROM pg_roles WHERE rolname LIKE 'leapview_%'"), "0")

    def test_foreign_role_resembling_a_system_name_is_ineligible(self):
        self.sql("CREATE ROLE pgX_foreign_role")
        self.bootstrap(succeeds=False)
        self.assertEqual(self.sql("SELECT count(*) FROM pg_roles WHERE rolname='pgx_foreign_role'"), "1")
        self.assertEqual(self.sql("SELECT count(*) FROM pg_roles WHERE rolname LIKE 'leapview_%'"), "0")

    def test_missing_malformed_public_linked_or_reused_credentials_fail_before_roles(self):
        path = self.credentials / PASSWORD_FILES[0]
        original = path.read_text()
        for scenario in ("missing", "short", "malformed", "public", "linked", "duplicate"):
            with self.subTest(scenario=scenario):
                path.unlink(missing_ok=True)
                if scenario == "linked":
                    path.symlink_to(self.credentials / PASSWORD_FILES[1])
                elif scenario != "missing":
                    path.write_text({"short": "short", "malformed": "x'" + "A" * 40,
                                     "duplicate": self.passwords[PASSWORD_FILES[1]]}.get(scenario, original))
                    path.chmod(0o644 if scenario == "public" else 0o600)
                self.bootstrap(succeeds=False)
                self.assertEqual(self.sql("SELECT count(*) FROM pg_roles WHERE rolname LIKE 'leapview_%'"), "0")
        path.unlink()
        path.write_text(original)
        path.chmod(0o600)
        self.bootstrap()

    def test_wrong_enrolled_cluster_identifier_refuses_before_mutation(self):
        self.bootstrap(succeeds=False, system_id=str(int(self.system_id) + 1))
        self.assertEqual(self.sql("SELECT count(*) FROM pg_roles WHERE rolname LIKE 'leapview_%'"), "0")

    def test_trusted_socket_without_peer_authentication_is_ineligible(self):
        (self.data / "pg_hba.conf").write_text("local all all trust\n")
        self.hba = (self.data / "pg_hba.conf").read_bytes()
        self.sql("SELECT pg_reload_conf()")
        # Each new psql connection sees the reloaded rules. Wait for the
        # authentication identity to change before asserting the refusal.
        import time
        for _ in range(30):
            if not self.sql("SELECT coalesce(system_user,'')").startswith("peer:"):
                break
            time.sleep(0.02)
        else:
            self.fail("HBA reload did not complete")
        self.bootstrap(succeeds=False)
        self.assertEqual(self.sql("SELECT count(*) FROM pg_roles WHERE rolname LIKE 'leapview_%'"), "0")


if __name__ == "__main__":
    unittest.main()
