"""Unprivileged contracts for the isolated real-application PostgreSQL helper."""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

import managed_application_support as support


class PostgreSQLSupportTest(unittest.TestCase):
    source = Path(__file__).resolve().parents[4]

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.home = self.root / "application"
        self.home.mkdir(mode=0o700)
        self.image = support.guest._locked_postgres_image(self.source)
        self.commands = []
        self.ready = True
        self.probe_output = None
        self.start_fails = False
        self.metadata = {
            "Id": "sha256:" + "a" * 64,
            "RepoDigests": [support.guest._postgres_repo_digest(self.image)],
            "Os": "linux", "Architecture": support.native_architecture(),
        }

    def completed(self, argv, code=0, stdout=""):
        return subprocess.CompletedProcess(argv, code, stdout, "")

    def docker(self, *argv, timeout=120, check=True):
        self.commands.append(("docker", argv))
        if argv[:2] == ("image", "inspect"):
            return self.completed(argv, stdout=json.dumps([self.metadata]))
        if argv[0] == "run":
            environment = Path(argv[argv.index("--env-file") + 1])
            self.assertEqual(environment.stat().st_mode & 0o777, 0o600)
            self.private_environment = environment.read_text()
            if self.start_fails:
                raise RuntimeError("private subprocess diagnostic " + self.private_environment)
            return self.completed(argv, stdout="b" * 64 + "\n")
        if argv[0:2] == ("container", "rm"):
            return self.completed(argv)
        self.fail("unexpected Docker operation " + repr(argv))

    def run_command(self, *argv, timeout=120, check=True):
        self.commands.append(("run", argv))
        if argv[0] == "openssl":
            for option in ("-out", "-keyout"):
                if option in argv:
                    Path(argv[argv.index(option) + 1]).write_text("fixture certificate bytes")
            return self.completed(argv)
        if argv[0] == "env":
            self.assertIn("DOCKER_HOST=unix:///run/support-docker.sock", argv)
            self.assertEqual(argv[1:5], ("-u", "DOCKER_CONTEXT", "-u", "DOCKER_TLS_VERIFY"))
            self.assertIn("PGSSLMODE=verify-full", argv[-1])
            self.assertIn("docker exec", argv[-1])
            return self.completed(argv, code=0 if self.ready else 1,
                                  stdout=self.probe_output if self.probe_output is not None else
                                  "\n".join(support.guest.TLS_ROLE_EXPECTATIONS.values()) + "\n")
        self.fail("unexpected subprocess " + repr(argv))

    def prepare(self, **overrides):
        options = dict(support_docker=self.docker, run=self.run_command, root=self.root,
                       source=self.source, image=self.image, home_base=self.home)
        options.update(overrides)
        return support.prepare_postgres(**options)

    def test_prepulled_identity_roles_tls_and_private_inputs(self):
        result = self.prepare()
        self.assertEqual(result["evidence"]["image"], self.image)
        self.assertEqual(result["evidence"]["repoDigests"], self.metadata["RepoDigests"])
        self.assertEqual(result["evidence"]["tlsRoleProbes"], list(support.guest.TLS_ROLE_EXPECTATIONS.values()))
        self.assertEqual(result["urls"], support.guest._postgres_connection_urls(result["credentials"]))
        start = next(argv for kind, argv in self.commands if kind == "docker" and argv[0] == "run")
        self.assertEqual(start[start.index("--network") + 1], "host")
        self.assertIn("--pull=never", start)
        self.assertIn(str(self.home) + ":/var/lib/leapview", start)
        self.assertIn("listen_addresses=172.30.0.1", start[-1])
        self.assertIn(support.guest._postgres_tls_entrypoint_script(), start[-1])
        self.assertTrue(any(value.endswith(":/docker-entrypoint-initdb.d/10-leapview-roles.sh:ro") for value in start))
        self.assertFalse(Path(start[start.index("--env-file") + 1]).exists())
        self.assertFalse(any(argv[0] in ("pull", "push", "load", "tag") for kind, argv in self.commands if kind == "docker"))
        retained = json.dumps(result["evidence"])
        for secret in result["credentials"].values():
            self.assertNotIn(secret, retained)
        self.assertFalse(list((self.root / "postgres" / "tls").glob("ca.key")))

    def test_wrong_source_image_rejected_before_any_subprocess(self):
        with self.assertRaisesRegex(support.SupportError, "source-locked"):
            self.prepare(image="postgres:latest")
        self.assertEqual(self.commands, [])

    def test_private_bind_parent_is_owned_before_entrypoint_drops_privileges(self):
        self.prepare()
        start = next(argv for kind, argv in self.commands if kind == "docker" and argv[0] == "run")
        self.assertEqual(start[start.index("--user") + 1], "0:0")
        script = start[-1]
        self.assertLess(script.index("chown postgres:postgres /var/lib/postgresql"),
                        script.index("exec /usr/local/bin/docker-entrypoint.sh"))
        self.assertEqual((self.root / "postgres/data").stat().st_mode & 0o777, 0o700)

    def test_wrong_local_digest_or_platform_cannot_start_postgres(self):
        for field, value in (("RepoDigests", []), ("Architecture", "wrong"), ("Id", "mutable")):
            with self.subTest(field=field), tempfile.TemporaryDirectory() as root:
                old = self.metadata[field]
                self.metadata[field] = value
                try:
                    with self.assertRaisesRegex(support.SupportError, "identity"):
                        self.prepare(root=Path(root))
                    self.assertFalse(any(argv[0] == "run" for kind, argv in self.commands if kind == "docker"))
                finally:
                    self.metadata[field] = old

    def test_failed_start_removes_env_and_redacts_diagnostic(self):
        self.start_fails = True
        with self.assertRaises(support.SupportError) as raised:
            self.prepare()
        self.assertNotIn("private subprocess", str(raised.exception))
        self.assertNotIn("PASSWORD", str(raised.exception))
        self.assertFalse((self.root / "postgres" / "postgres.env").exists())
        self.assertTrue(any(argv[:2] == ("container", "rm") for kind, argv in self.commands if kind == "docker"))

    def test_partial_or_failed_tls_probe_is_not_readiness(self):
        self.ready = False
        with self.assertRaisesRegex(support.SupportError, "TLS runtime-role"):
            self.prepare()
        self.assertFalse((self.root / "postgres" / "postgres.env").exists())
        self.assertTrue(any(argv[:2] == ("container", "rm") for kind, argv in self.commands if kind == "docker"))

    def test_success_exit_with_only_one_tls_role_is_rejected(self):
        self.probe_output = next(iter(support.guest.TLS_ROLE_EXPECTATIONS.values())) + "\n"
        with self.assertRaisesRegex(support.SupportError, "TLS runtime-role"):
            self.prepare()
        self.assertTrue(any(argv[:2] == ("container", "rm") for kind, argv in self.commands if kind == "docker"))

    def test_existing_fixture_or_relative_socket_fails_without_overwrite(self):
        sentinel = self.root / "postgres"
        sentinel.mkdir(mode=0o700)
        (sentinel / "keep").write_text("existing")
        with self.assertRaises(support.SupportError):
            self.prepare()
        self.assertEqual((sentinel / "keep").read_text(), "existing")
        with self.assertRaises(support.SupportError):
            self.prepare(support_socket=Path("relative.sock"))


if __name__ == "__main__":
    unittest.main()
