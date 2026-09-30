"""Safety regressions for the privileged Docker fixture; no root required."""

import json
import unittest
from unittest.mock import patch

import docker_daemon_test as fixture


class NamespaceSafetyTest(unittest.TestCase):
    def setUp(self):
        self.parent = {kind: f"{kind}:[1]" for kind in ("mnt", "net", "pid")}
        self.child = {kind: f"{kind}:[2]" for kind in self.parent}

    def verify(self, namespaces, uid=0, pid=1):
        with (
            patch.object(fixture.os, "geteuid", return_value=uid),
            patch.object(fixture.os, "getpid", return_value=pid),
            patch.object(fixture.os, "readlink", side_effect=lambda path: namespaces[path.rsplit("/", 1)[1]]),
        ):
            fixture.verify_namespaces(self.parent)

    def test_refuses_each_shared_namespace_before_mutating_mounts(self):
        for kind in self.parent:
            with self.subTest(namespace=kind):
                shared = self.child | {kind: self.parent[kind]}
                with self.assertRaisesRegex(SystemExit, "isolated"):
                    self.verify(shared)

    def test_requires_root_and_namespace_init(self):
        for uid, pid in ((1000, 1), (0, 42)):
            with self.subTest(uid=uid, pid=pid):
                with self.assertRaises(SystemExit):
                    self.verify(self.child, uid, pid)

    def test_accepts_fresh_namespaces(self):
        self.verify(self.child)

    def test_entrypoint_refuses_shared_mounts_before_any_system_command(self):
        arguments = ["fixture", "image.tar", "--workdir", ".", "--docker-package", ".",
                     "--namespace-parent", json.dumps(self.parent)]
        with (
            patch.object(fixture.sys, "argv", arguments),
            patch.object(fixture.os, "geteuid", return_value=0),
            patch.object(fixture.os, "getpid", return_value=1),
            patch.object(fixture, "namespace_ids", return_value=self.child | {"mnt": self.parent["mnt"]}),
            patch.object(fixture.subprocess, "run") as command,
        ):
            with self.assertRaisesRegex(SystemExit, "isolated"):
                fixture.main()
            command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
