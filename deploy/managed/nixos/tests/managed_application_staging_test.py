"""Process and identity contracts for online staging; no daemon is launched."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import managed_application_staging as staging


class FakeDaemon:
    def __init__(self, fail=False, shutdown_fails=False):
        self.fail, self.shutdown_fails = fail, shutdown_fails
        self.terminated = self.killed = self.waited = False

    def poll(self):
        return 1 if self.fail else None

    def terminate(self):
        self.terminated = True

    def kill(self):
        self.killed = True

    def wait(self, timeout):
        self.waited = True
        if self.shutdown_fails and not self.killed:
            raise subprocess.TimeoutExpired("private-daemon", timeout)
        return 0


class StagingTest(unittest.TestCase):
    repository = Path(__file__).resolve().parents[4]

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(dir="/tmp")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.source = self.root / "source"
        for name in ("deploy/managed/nixos/flake.nix", "deploy/compose/deployment.env.example",
                     "internal/app/cli/composectl/qualification_native_postgres.go",
                     "deploy/compose/qualification/Dockerfile.authoring-client",
                     "deploy/compose/qualification/package.json", "deploy/compose/qualification/package-lock.json"):
            target = self.source / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(self.repository / name, target)
        (self.source / "deploy/compose/qualification/Dockerfile.authoring-browser").write_text("FROM fixture@sha256:" + "f"*64 + "\n")
        self.docker_package = self.root / "docker-package"
        (self.docker_package / "bin").mkdir(parents=True)
        for executable in ("docker", "dockerd"):
            path = self.docker_package / "bin" / executable
            path.write_text("#!/bin/sh\nexit 1\n")
            path.chmod(0o755)
        self.selections = {role: {"image": "ghcr.io/flidai/leapview@sha256:"+digit*64,
                                  "sourceRevision": digit*40}
                           for role, digit in (("bootstrap","a"), ("predecessor","b"), ("candidate","c"))}
        self.processes, self.launches, self.commands = [], [], []
        self.failure = None
        self.bad_identity = False
        self.bad_alias = False
        self.bad_revision = False

    def launch(self, argv, **kwargs):
        self.launches.append((argv, kwargs))
        process = FakeDaemon(fail=self.failure == "second-start" and len(self.processes) == 1,
                             shutdown_fails=self.failure == "shutdown" and len(self.processes) == 0)
        self.processes.append(process)
        return process

    def execute(self, argv, **kwargs):
        self.commands.append((argv, kwargs))
        action = argv[3:]
        if self.failure == "pull" and action[0] == "pull":
            return subprocess.CompletedProcess(argv, 1, "private-registry-output", "private-registry-output")
        if self.failure == "build" and action[0] == "build":
            return subprocess.CompletedProcess(argv, 1, "private-build-output", "private-build-output")
        if action[:2] == ["image", "inspect"]:
            reference = action[-1]
            identity = "sha256:" + "d"*64
            digests, revision = [], ""
            if "@sha256:" in reference:
                repository, digest = reference.split("@")
                repository = repository.split(":")[0]
                if repository == "docker.io/library/postgres": repository = "postgres"
                digests = [repository+"@"+digest]
                for selected in self.selections.values():
                    if selected["image"] == reference:
                        revision = selected["sourceRevision"]
                        if self.bad_revision: revision = "e"*40
                if self.bad_identity: digests = []
            elif reference == "basecamp/kamal-proxy:v0.9.2" and self.bad_alias:
                identity = "sha256:" + "e"*64
            metadata = {"Id": identity, "RepoDigests":digests, "Os":"linux", "Architecture":"amd64",
                        "Config":{"Labels":{"org.opencontainers.image.revision":revision}}}
            return subprocess.CompletedProcess(argv, 0, json.dumps([metadata]), "")
        return subprocess.CompletedProcess(argv, 0, "", "")

    def stage(self):
        with patch.object(staging, "_verify_staging_namespace"):
            return staging._stage_images_in_namespace(root=self.root, source=self.source,
                                        docker_package=self.docker_package, selections=self.selections,
                                        parent_namespaces={"mnt":"parent-mnt", "net":"parent-net", "pid":"parent-pid"},
                                        run=self.execute, popen=self.launch)

    def test_host_namespace_rejected_before_any_daemon_or_file_mutation(self):
        with patch.object(staging, "namespace_ids", return_value={"mnt":"same", "net":"same", "pid":"same"}), \
                patch.object(staging.os, "getpid", return_value=1), patch.object(staging.os, "geteuid", return_value=0):
            with self.assertRaisesRegex(staging.StagingError, "isolated"):
                staging._stage_images_in_namespace(root=self.root, source=self.source,
                    docker_package=self.docker_package, selections=self.selections,
                    parent_namespaces={"mnt":"same", "net":"same", "pid":"same"},
                    run=self.execute, popen=self.launch)
        self.assertEqual(self.launches, [])
        self.assertFalse((self.root / "staging").exists())

    def test_every_namespace_and_pid_one_required(self):
        parent = {"mnt":"host-mnt", "net":"host-net", "pid":"host-pid"}
        child = {key:"child-"+key for key in parent}
        for kind in parent:
            with self.subTest(kind=kind), patch.object(staging, "namespace_ids", return_value={**child, kind:parent[kind]}), \
                    patch.object(staging.os, "getpid", return_value=1), patch.object(staging.os, "geteuid", return_value=0):
                with self.assertRaisesRegex(staging.StagingError, "isolated"):
                    staging._verify_staging_namespace(parent)
        with patch.object(staging, "namespace_ids", return_value=child), \
                patch.object(staging.os, "getpid", return_value=123), patch.object(staging.os, "geteuid", return_value=0):
            with self.assertRaises(staging.StagingError): staging._verify_staging_namespace(parent)
        with patch.object(staging, "namespace_ids", return_value=child), \
                patch.object(staging.os, "getpid", return_value=1), patch.object(staging.os, "geteuid", return_value=0):
            staging._verify_staging_namespace(parent)

    def orchestration(self, failure=None):
        tools = self.root / "tools"
        (tools / "bin").mkdir(parents=True)
        for name in ("unshare", "slirp4netns", "mount", "ip"):
            path = tools / "bin" / name
            path.write_text("test-only")
            path.chmod(0o700)
        trust = self.root / "test-ca.pem"
        trust.write_text("public CA fixture")
        events, processes = [], []
        outer = self
        class Process:
            pid = 12345
            returncode = None
            terminated = False
            def __init__(self, name): self.name = name
            def poll(self): return self.returncode
            def wait(self, timeout):
                events.append("wait-"+self.name)
                self.returncode = 1 if failure == self.name+"-exit" else 0
                if self.name == "worker" and not self.terminated:
                    (outer.root / "staging-control/result.json").write_text(json.dumps({"preserved":True}))
                return self.returncode
            def terminate(self):
                self.terminated = True
                events.append("stop-"+self.name)
            def kill(self): self.terminated = True
        def launch(argv, **options):
            name = "worker" if Path(argv[0]).name == "unshare" else "helper"
            events.append("start-"+name)
            process = Process(name)
            processes.append(process)
            self.launches.append((argv, options))
            return process
        def ready(descriptor, process):
            events.append("ready-"+process.name)
            if failure == process.name+"-ready":
                raise staging.StagingError("fixture readiness failure")
        def worker_pid(*args):
            events.append("verify-child")
            if failure == "namespace": raise staging.StagingError("fixture namespace failure")
            return 23456
        try:
            with patch.object(staging, "_require_parent_root"), patch.object(staging, "_await_ready", side_effect=ready), \
                    patch.object(staging, "_worker_pid", side_effect=worker_pid), \
                    patch.object(staging.os, "write", side_effect=lambda *_: events.append("release-worker")), \
                    patch.dict(os.environ, {"SSL_CERT_FILE":str(trust), "GH_TOKEN":"must-not-leak", "DOCKER_HOST":"host"}):
                result = staging.stage_images(root=self.root, source=self.source, docker_package=self.docker_package,
                    tools=tools, selections=self.selections, popen=launch)
            return result
        finally:
            self.orchestration_events, self.orchestration_processes = events, processes

    def test_parent_releases_worker_only_after_verified_namespace_and_slirp_ready(self):
        result = self.orchestration()
        self.assertEqual(self.orchestration_events, ["start-worker", "ready-worker", "verify-child",
            "start-helper", "ready-helper", "release-worker", "wait-worker", "wait-helper"])
        self.assertTrue(result["onlineNamespaceIsolated"] and result["userspaceNetworkStopped"])
        worker, helper = [argv for argv, _ in self.launches]
        for flag in ("--mount", "--net", "--pid", "--fork", "--mount-proc", "--kill-child=SIGKILL"):
            self.assertIn(flag, worker)
        self.assertIn("--disable-host-loopback", helper)
        self.assertEqual(helper[-2:], ["23456", "tap0"])
        for _, options in self.launches:
            self.assertNotIn("GH_TOKEN", options["env"])
            self.assertNotIn("DOCKER_HOST", options["env"])
        self.assertFalse(any("dockerd" in argv[0] for argv, _ in self.launches))

    def test_parent_readiness_failures_never_release_daemon_worker(self):
        for failure in ("worker-ready", "namespace", "helper-ready"):
            with self.subTest(failure=failure):
                for name in ("tools", "staging-control"):
                    shutil.rmtree(self.root/name, ignore_errors=True)
                self.launches = []
                with self.assertRaises(staging.StagingError): self.orchestration(failure)
                self.assertNotIn("release-worker", self.orchestration_events)
                self.assertTrue(all(process.terminated for process in self.orchestration_processes))

    def test_worker_or_slirp_failure_never_returns_preserved_stores(self):
        for failure in ("worker-exit", "helper-exit"):
            with self.subTest(failure=failure):
                for name in ("tools", "staging-control"):
                    shutil.rmtree(self.root/name, ignore_errors=True)
                with self.assertRaises(staging.StagingError): self.orchestration(failure)
                self.assertTrue(all(process.poll() is not None for process in self.orchestration_processes))

    def assert_stopped(self):
        self.assertEqual(len(self.processes), 2)
        self.assertTrue(all(process.terminated and process.waited for process in self.processes))

    def test_preserves_exact_daemon_roots_and_image_identities(self):
        with patch.dict(os.environ, {"DOCKER_HOST":"unix:///run/docker.sock", "DOCKER_CONTEXT":"host",
                                     "DOCKER_CONFIG":"/root/host-auth", "DOCKER_TLS_VERIFY":"1"}):
            result = self.stage()
        self.assert_stopped()
        self.assertNotEqual(result["appDataRoot"], result["supportDataRoot"])
        for root in (result["appDataRoot"], result["supportDataRoot"]):
            self.assertTrue(Path(root).is_dir())
        for argv, options in self.launches:
            for flag in ("--bridge=none", "--iptables=false", "--ip6tables=false", "--ip-forward=false", "--ip-masq=false"):
                self.assertIn(flag, argv)
            for key in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY"):
                self.assertNotIn(key, options["env"])
            self.assertNotEqual(options["env"]["DOCKER_CONFIG"], "/root/host-auth")
        for role, selected in self.selections.items():
            self.assertEqual(result["images"][role]["image"], selected["image"])
            self.assertIn(selected["image"], result["images"][role]["repoDigests"])
        for image in result["images"].values():
            self.assertIn(image["repositoryDigest"], image["repoDigests"])
        builds = [argv for argv, _ in self.commands if argv[3] == "build"]
        self.assertEqual(len(builds), 2)
        self.assertTrue(all("--network=host" in argv and "--platform=linux/amd64" in argv for argv in builds))
        self.assertTrue(any("LEAPVIEW_IMAGE="+self.selections["bootstrap"]["image"] in argv for argv in builds))
        self.assertEqual(set(result["helpers"]), {"clientImage", "browserImage"})
        for argv, _ in self.commands:
            self.assertEqual(argv[1], "--host")
            self.assertTrue(argv[2].startswith("unix://"+str(self.root)+"/staging/"))
            self.assertNotIn(argv[3], ("load", "save", "push", "run"))

    def test_pull_build_and_second_start_failures_stop_both_daemons(self):
        for failure in ("pull", "build", "second-start"):
            with self.subTest(failure=failure):
                if (self.root / "staging").exists(): shutil.rmtree(self.root / "staging")
                self.processes, self.launches, self.commands = [], [], []
                self.failure = failure
                with self.assertRaises(staging.StagingError) as raised:
                    self.stage()
                self.assertNotIn("private-registry-output", str(raised.exception))
                self.assertNotIn("private-build-output", str(raised.exception))
                self.assert_stopped()

    def test_identity_mismatch_fails_before_helper_builds(self):
        self.bad_identity = True
        with self.assertRaisesRegex(staging.StagingError, "identity"):
            self.stage()
        self.assert_stopped()
        self.assertFalse(any(argv[3] == "build" for argv, _ in self.commands))

    def test_source_revision_mismatch_and_wrong_proxy_alias_are_rejected(self):
        for variant in ("bad_revision", "bad_alias"):
            with self.subTest(variant=variant):
                if (self.root / "staging").exists(): shutil.rmtree(self.root / "staging")
                self.processes, self.launches, self.commands = [], [], []
                setattr(self, variant, True)
                with self.assertRaisesRegex(staging.StagingError, "identity"):
                    self.stage()
                self.assert_stopped()
                self.assertFalse(any(argv[3] == "build" for argv, _ in self.commands))
                setattr(self, variant, False)

    def test_shutdown_error_does_not_skip_the_other_daemon(self):
        launch = self.launch
        def fail_termination(argv, **kwargs):
            process = launch(argv, **kwargs)
            if len(self.processes) == 2:
                def reject():
                    raise ProcessLookupError("already exited")
                process.terminate = reject
            return process
        with patch.object(self, "launch", side_effect=fail_termination):
            self.stage()
        self.assertTrue(self.processes[0].terminated)
        self.assertTrue(all(process.waited for process in self.processes))

    def test_shutdown_timeout_is_not_successful_preserved_state(self):
        self.failure = "shutdown"
        with self.assertRaisesRegex(staging.StagingError, "shutdown"):
            self.stage()
        self.assert_stopped()
        self.assertTrue(self.processes[0].killed)

    def test_existing_stage_is_never_reused_or_modified(self):
        stage = self.root / "staging"
        stage.mkdir(mode=0o700)
        (stage / "sentinel").write_text("existing")
        with self.assertRaises(staging.StagingError): self.stage()
        self.assertEqual((stage / "sentinel").read_text(), "existing")
        self.assertEqual(self.launches, [])

    def test_mutable_application_image_rejected_before_launch(self):
        self.selections["candidate"]["image"] = "ghcr.io/flidai/leapview:main"
        with self.assertRaises(staging.StagingError): self.stage()
        self.assertEqual(self.launches, [])


if __name__ == "__main__":
    unittest.main()
