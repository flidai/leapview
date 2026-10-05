import json
from pathlib import Path
import sys
import urllib.parse
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

import nix_compose_host_guest as host_guest
import nix_compose_qualification as qualification


IMAGE = "ghcr.io/flidai/leapview@sha256:" + "a" * 64
REVISION = "1" * 40
BOOT_BEFORE = "11111111-1111-4111-8111-111111111111"
BOOT_AFTER = "22222222-2222-4222-8222-222222222222"
RUNTIME = {
    "product": "leapviewctl",
    "version": "1.2.3",
    "revision": REVISION,
    "buildTime": "2026-10-04T05:06:07Z",
    "dirty": False,
    "development": False,
}
POSTGRES_IMAGE = "docker.io/library/postgres:18-alpine@sha256:" + "8" * 64


class HostGuestReceiptTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.evidence = self.root / "evidence"
        self.evidence.mkdir()
        self.image_id = "sha256:" + "e" * 64
        self.marker_sha = "sha256:" + "f" * 64
        self.nix_sha = "sha256:" + "c" * 64
        self.oci_sha = "sha256:" + "d" * 64
        self.bootstrap_sha = "sha256:" + "9" * 64
        self.manifest = {
            "schemaVersion": 1,
            "nonce": "3" * 32,
            "sourceCloudImageSHA256": "sha256:" + "b" * 64,
            "guestOS": "ubuntu2404",
            "architecture": "amd64",
            "virtualizationMode": "tcg",
        }
        self.manifest_bytes = json.dumps(self.manifest, sort_keys=True).encode() + b"\n"
        self._write("launcher-manifest.json", self.manifest_bytes)
        self.known_hosts_sha = "sha256:" + "6" * 64
        self.launcher_receipt = {
            "schemaVersion": 1,
            "scope": "nix-compose-guest-launcher",
            "result": "ready",
            **self.manifest,
            "manifestSHA256": host_guest._digest(self.manifest_bytes),
            "inputs": {
                "cloudImageSHA256": self.manifest["sourceCloudImageSHA256"],
                "firmwareSHA256": None,
                "userDataSHA256": "sha256:" + "7" * 64,
                "metaDataSHA256": "sha256:" + "8" * 64,
                "seedISOSHA256": "sha256:" + "9" * 64,
                "sshClientPublicKeySHA256": "sha256:" + "a" * 64,
                "sshHostPublicKeySHA256": "sha256:" + "b" * 64,
                "knownHostsSHA256": self.known_hosts_sha,
            },
            "runner": {
                "hostArchitecture": "x86_64",
                "qemuSystemBinarySHA256": "sha256:" + "c" * 64,
                "qemuVersionSHA256": "sha256:" + "d" * 64,
                "accelerator": "tcg",
            },
        }
        self.launcher_bytes = json.dumps(self.launcher_receipt, sort_keys=True).encode() + b"\n"
        self._write("launcher-receipt.json", self.launcher_bytes)
        self._write("host-kvm-probe.json", b'{"apiVersion":null,"available":false}\n')
        self._write("host-runner-architecture.txt", b"x86_64\n")
        self.pool_artifacts = {
            "schema_version": 1,
            "pool": {"storage_location": "/var/lib/leapview/home/managed-data"},
            "evidence": {"schema_version": 1, "evidence": {"checks": []}},
        }
        self.pool_artifact_bytes = json.dumps(self.pool_artifacts, sort_keys=True).encode() + b"\n"
        self.pool_artifacts_sha = host_guest._digest(self.pool_artifact_bytes)
        self.postgres_fixture = {
            "image": POSTGRES_IMAGE,
            "repoDigest": "postgres@sha256:" + "8" * 64,
            "imageID": "sha256:" + "7" * 64,
            "repoDigests": ["postgres@sha256:" + "8" * 64],
            "platform": "linux/amd64",
            "containerName": "leapview-qualification-pg-" + "3" * 16,
            "network": "leapview_default",
            "stateVolume": "leapview_leapview-state",
            "dataVolume": "leapview-qualification-pg-" + "3" * 16,
            "restartPolicy": "unless-stopped",
            "initScriptSHA256": "sha256:" + "4" * 64,
            "tlsRoleProbesBeforeInstall": host_guest.TLS_ROLE_EXPECTATIONS,
        }
        self.pg_container_id = "5" * 64
        self.pg_started_before = "2026-10-04T12:00:00.000000000Z"
        self.pg_started_after = "2026-10-04T12:05:00.000000000Z"
        self._write("postgres-image-repo-digests.json", json.dumps(["postgres@sha256:" + "8" * 64]).encode() + b"\n")
        self._write("postgres-image-id.txt", (self.postgres_fixture["imageID"] + "\n").encode())
        self._write("postgres-image-platform.txt", b"linux/amd64\n")
        self._write("postgres-compose-network-labels.txt", b"leapview default\n")
        self._write("postgres-compose-state-volume-labels.txt", b"leapview leapview-state\n")
        self._write("pool-probe-state-volume-owner.txt", b"999:999:999:999:755\n")
        self._write("postgres-fixture.json", json.dumps(self.postgres_fixture, sort_keys=True).encode() + b"\n")
        self._write("postgres-container-before-reboot.txt", (
            f"{self.pg_container_id} {self.postgres_fixture['imageID']} running unless-stopped {self.pg_started_before}\n"
        ).encode())
        self._write("postgres-container-after-reboot.txt", (
            f"{self.pg_container_id} {self.postgres_fixture['imageID']} running unless-stopped {self.pg_started_after}\n"
        ).encode())
        self._write("postgres-tls-role-probes-before-install.txt", (
            "\n".join(host_guest.TLS_ROLE_EXPECTATIONS.values()) + "\n"
        ).encode())
        self._write("postgres-tls-role-probe-after-reboot.txt", (
            host_guest.TLS_ROLE_EXPECTATIONS["controlRuntime"] + "\n"
        ).encode())
        self._write("pool-probe-order.json", json.dumps({
            "schemaVersion": 1,
            "freshInstallRootAbsentBeforeProbe": True,
            "servingComposeServiceStartedBeforeInstall": False,
            "tlsRuntimeRolesProbedBeforeInstall": True,
            "canonicalPoolCommand": "admin delivery pool qualify",
            "candidateImage": IMAGE,
            "candidateImageID": self.image_id,
            "candidatePlatform": "linux/amd64",
            "sourceRevision": REVISION,
            "operatorBootstrapWrittenAfterPoolProbe": True,
        }, sort_keys=True).encode() + b"\n")
        self._write("pool-probe-preconditions.txt", b"fresh-root-absent\nserving-service-not-running\n")
        self._write("pool-probe-completed-before-install.txt", b"completed-before-install\n")
        self._write("physical-pool-qualification-artifacts.json", self.pool_artifact_bytes)
        self._write("physical-pool-qualification-sha256.txt", (self.pool_artifacts_sha + "\n").encode())
        self._write("serving-credential-boundary.json", json.dumps({
            "controlMigratorURLAbsentFromServingEnvironment": True,
            "duckLakeMigratorURLAbsentFromServingEnvironment": True,
        }, sort_keys=True).encode() + b"\n")
        self._write("operator-bootstrap-cleanup.txt", b"absent\n")
        self._write("database-secret-boundary.json", json.dumps({
            "poolProbeUsedSeparatePrivateEnvironment": True,
            "operationOnlyMigratorURLsAbsentFromServingEnvironment": True,
            "operatorBootstrapRemovedAfterSuccessfulInstall": True,
            "candidateSecretsExcludedFromRetainedEvidence": True,
        }, sort_keys=True).encode() + b"\n")
        self._write("qualification-report.json", json.dumps({
            "schemaVersion": 1,
            "scope": host_guest.SCOPE,
            "result": "passed",
            "guestOS": "ubuntu2404",
            "platform": "linux/amd64",
            "image": IMAGE,
            "sourceRevision": REVISION,
            "releaseAdmission": False,
            "postgresFixtureImage": POSTGRES_IMAGE,
            "postgresFixturePlatform": "linux/amd64",
            "physicalPoolArtifactsSHA256": self.pool_artifacts_sha,
        }).encode() + b"\n")
        self._write("ssh-root.txt", b"root\n")
        self._write("fresh-install-state.txt", b"absent\n")
        os_release = b'ID=ubuntu\nVERSION_ID="24.04"\n'
        self._write("preinstall-os-release.txt", os_release)
        self._write("postreboot-os-release.txt", os_release)
        self._write("preinstall-architecture.txt", b"x86_64\n")
        self._write("postreboot-architecture.txt", b"x86_64\n")
        self._write("preinstall-boot-id.txt", (BOOT_BEFORE + "\n").encode())
        self._write("postreboot-boot-id.txt", (BOOT_AFTER + "\n").encode())
        self._write("preinstall-kernel.txt", b"6.8.0-test\n")
        self._write("postreboot-kernel.txt", b"6.8.0-test\n")
        self._write("postreboot-pid1.txt", b"/usr/lib/systemd/systemd\n")
        self._write("docker-enabled.txt", b"enabled\n")
        self._write("docker-active.txt", b"active\n")
        self._write("oci-repo-digests.json", json.dumps([IMAGE]).encode() + b"\n")
        self._write("oci-image-id.txt", (self.image_id + "\n").encode())
        self._write("nix-controller-sha256.txt", (self.nix_sha + "\n").encode())
        self._write("nix-controller-runtime.json", json.dumps(RUNTIME).encode() + b"\n")
        self._write("nix-controller-help.txt", b"leapviewctl host install\n")
        self._write("oci-payload-controller-sha256.txt", (self.oci_sha + "\n").encode())
        self._write("host-install-exit-code.txt", b"0\n")
        self._write("host-marker-sha256.txt", (self.marker_sha + "\n").encode())
        self._write("host-marker-projection.json", json.dumps({
            "schemaVersion": 1, "image": IMAGE, "https": True, "markerSHA256": self.marker_sha,
        }, sort_keys=True).encode() + b"\n")
        self._write("docker-inspect-placeholder.txt", b"unused\n")
        self.current_target = "releases/sha256-" + "a" * 64
        self.wrapper_target = "../../../opt/leapview/current/leapviewctl"
        self._write("current-generation-link.txt", (self.current_target + "\n").encode())
        self._write("controller-wrapper-link.txt", (self.wrapper_target + "\n").encode())
        self._write("installed-controller-sha256.txt", (self.oci_sha + "\n").encode())
        self._write("installed-controller-runtime.json", json.dumps(RUNTIME).encode() + b"\n")
        self.container = {
            "id": "4" * 64,
            "configuredImage": IMAGE,
            "imageID": self.image_id,
            "state": "running",
            "health": "healthy",
            "project": "leapview",
            "service": "leapview",
            "repoDigests": [IMAGE],
            "startedAtBeforeReboot": "2026-10-04T12:00:00.000000000Z",
            "startedAtAfterReboot": "2026-10-04T12:05:00.000000000Z",
        }
        self.inspect_before = " ".join([
            self.container["id"], self.container["configuredImage"], self.container["imageID"],
            "running", "healthy", "leapview", "leapview", self.container["startedAtBeforeReboot"],
        ])
        self.inspect_after = " ".join([
            self.container["id"], self.container["configuredImage"], self.container["imageID"],
            "running", "healthy", "leapview", "leapview", self.container["startedAtAfterReboot"],
        ])
        self._write("prereboot-container-inspect.txt", (self.inspect_before + "\n").encode())
        self._write("container-inspect.txt", (self.inspect_after + "\n").encode())
        self._write("automatic-restart-probes.json", json.dumps([{
            "elapsedSeconds": 0.0, "inspection": self.inspect_after,
        }]).encode() + b"\n")
        self._write("controller-status.txt", b"0\n")

    def tearDown(self):
        self.temporary.cleanup()

    def _write(self, name, data):
        (self.evidence / name).write_bytes(data)

    def _receipt(self, install_mode="nix-controller"):
        driver = "source-bootstrap-linux.sh" if install_mode == "bootstrap" else "nix-archive"
        self._write("installer-driver.txt", (driver + "\n").encode())
        if install_mode == "bootstrap":
            self._write("source-bootstrap-sha256.txt", (self.bootstrap_sha + "\n").encode())
            self._write("guest-bootstrap-sha256.txt", (self.bootstrap_sha + "\n").encode())
        self._write("qualification-report.json", json.dumps({
            "schemaVersion": 1,
            "scope": host_guest.SCOPE,
            "result": "passed",
            "guestOS": "ubuntu2404",
            "platform": "linux/amd64",
            "installMode": install_mode,
            "installerDriver": driver,
            "image": IMAGE,
            "sourceRevision": REVISION,
            "releaseAdmission": False,
            "postgresFixtureImage": POSTGRES_IMAGE,
            "postgresFixturePlatform": "linux/amd64",
            "physicalPoolArtifactsSHA256": self.pool_artifacts_sha,
        }, sort_keys=True).encode() + b"\n")
        generation = "sha256-" + "a" * 64
        guest = {
            "nonceSHA256": host_guest._digest(self.manifest["nonce"].encode()),
            "manifestSHA256": host_guest._digest(self.manifest_bytes),
            "sourceCloudImageSHA256": self.manifest["sourceCloudImageSHA256"],
            "os": {"id": "ubuntu", "versionID": "24.04"},
            "architecture": "x86_64",
            "kernelBefore": "6.8.0-test",
            "kernelAfter": "6.8.0-test",
            "bootIDBefore": BOOT_BEFORE,
            "bootIDAfter": BOOT_AFTER,
            "systemdPID1": "/usr/lib/systemd/systemd",
            "docker": {"enabled": "enabled", "active": "active"},
            "container": self.container,
            "markerSHA256": self.marker_sha,
            "generation": generation,
            "links": {"current": self.current_target, "controllerWrapper": self.wrapper_target},
            "controllers": {
                "nixArchiveSHA256": self.nix_sha,
                "ociPayloadSHA256": self.oci_sha,
                "installedSHA256": self.oci_sha,
                "nixRuntimeIdentity": RUNTIME,
                "installedRuntimeIdentity": RUNTIME,
            },
            "postgresFixture": self.postgres_fixture,
            "physicalPoolArtifactsSHA256": self.pool_artifacts_sha,
        }
        self._write("postreboot-boot-id.txt", (BOOT_AFTER + "\n").encode())
        inventory = qualification._qualification_evidence_inventory(self.evidence)
        return {
            "schemaVersion": 1,
            "scope": host_guest.SCOPE,
            "result": "passed",
            "releaseAdmission": False,
            "identity": {
                "archiveSHA256": "sha256:" + "1" * 64,
                "controllerSHA256": self.nix_sha,
                "controllerBuildIdentitySHA256": "sha256:" + "2" * 64,
                "image": IMAGE,
                "sourceRevision": REVISION,
                "platform": "linux/amd64",
                "installMode": install_mode,
                "installerDriver": driver,
                "sourceBootstrapSHA256": self.bootstrap_sha if install_mode == "bootstrap" else None,
                "releaseIdentitySHA256": "sha256:" + "5" * 64,
            },
            "runner": {
                "hostArchitecture": "x86_64",
                "guestArchitecture": "x86_64",
                "sshEndpoint": "127.0.0.1:2222",
                "virtualizationMode": "tcg",
                "tcgEnabled": True,
                "kvm": {"available": False, "apiVersion": None},
                "sshKnownHostsSHA256": self.known_hosts_sha,
                "launcherReceiptSHA256": host_guest._digest(self.launcher_bytes),
                "launcherInputs": self.launcher_receipt["inputs"],
                "qemuSystemBinarySHA256": self.launcher_receipt["runner"]["qemuSystemBinarySHA256"],
                "qemuVersionSHA256": self.launcher_receipt["runner"]["qemuVersionSHA256"],
            },
            "guest": guest,
            "assertions": {assertion: True for assertion in host_guest.ASSERTIONS},
            "excludedGates": ["two-image-upgrade-and-rollback", "full-enterprise-publication-journey"],
            "evidenceInventory": inventory,
        }

    def test_receipt_binds_reboot_and_oci_controller_measurements(self):
        receipt = self._receipt()
        self.assertIs(host_guest._validate_receipt(receipt, self.evidence), receipt)

        invalid = json.loads(json.dumps(receipt))
        invalid["guest"]["controllers"]["installedSHA256"] = "sha256:" + "9" * 64
        with self.assertRaisesRegex(host_guest.HostGuestError, "conflates"):
            host_guest._validate_receipt(invalid, self.evidence)

        invalid = json.loads(json.dumps(receipt))
        invalid["releaseAdmission"] = True
        with self.assertRaisesRegex(host_guest.HostGuestError, "non-admitting"):
            host_guest._validate_receipt(invalid, self.evidence)

        invalid = json.loads(json.dumps(receipt))
        self._write("preinstall-boot-id.txt", (BOOT_AFTER + "\n").encode())
        invalid["evidenceInventory"] = qualification._qualification_evidence_inventory(self.evidence)
        with self.assertRaisesRegex(host_guest.HostGuestError, "preinstall-boot-id.txt"):
            host_guest._validate_receipt(invalid, self.evidence)

    def test_receipt_binds_canonical_pool_probe_to_candidate_and_fresh_target(self):
        receipt = self._receipt()
        self.assertIs(host_guest._validate_receipt(receipt, self.evidence), receipt)

        invalid = json.loads(json.dumps(receipt))
        invalid["guest"]["postgresFixture"]["imageID"] = "sha256:" + "9" * 64
        with self.assertRaisesRegex(host_guest.HostGuestError, "postgres-image-id.txt"):
            host_guest._validate_receipt(invalid, self.evidence)

        order_path = self.evidence / "pool-probe-order.json"
        original = order_path.read_bytes()
        order = json.loads(original)
        order["candidateImageID"] = "sha256:" + "9" * 64
        order_path.write_text(json.dumps(order, sort_keys=True) + "\n")
        invalid = json.loads(json.dumps(receipt))
        invalid["evidenceInventory"] = qualification._qualification_evidence_inventory(self.evidence)
        with self.assertRaisesRegex(host_guest.HostGuestError, "fresh, non-serving"):
            host_guest._validate_receipt(invalid, self.evidence)
        order_path.write_bytes(original)

        pool_path = self.evidence / "physical-pool-qualification-artifacts.json"
        pool_bytes = pool_path.read_bytes()
        pool_path.unlink()
        with self.assertRaises(host_guest.HostGuestError):
            host_guest._validate_receipt(receipt, self.evidence)
        pool_path.write_bytes(pool_bytes)

    def test_arm_launcher_receipt_requires_locked_firmware_hash(self):
        manifest = {**self.manifest, "architecture": "arm64"}
        manifest_bytes = json.dumps(manifest, sort_keys=True).encode() + b"\n"
        receipt = json.loads(self.launcher_bytes)
        receipt.update({
            "architecture": "arm64",
            "manifestSHA256": host_guest._digest(manifest_bytes),
        })
        receipt["inputs"]["firmwareSHA256"] = "sha256:" + "f" * 64
        receipt["runner"].update({"hostArchitecture": "aarch64"})
        valid = json.dumps(receipt, sort_keys=True).encode() + b"\n"
        self.assertEqual(
            host_guest._validate_launcher_receipt(
                valid, manifest=manifest, manifest_bytes=manifest_bytes,
                known_hosts_sha256=self.known_hosts_sha,
            )["inputs"]["firmwareSHA256"],
            "sha256:" + "f" * 64,
        )

        receipt["inputs"]["firmwareSHA256"] = None
        invalid = json.dumps(receipt, sort_keys=True).encode() + b"\n"
        with self.assertRaisesRegex(host_guest.HostGuestError, "firmwareSHA256"):
            host_guest._validate_launcher_receipt(
                invalid, manifest=manifest, manifest_bytes=manifest_bytes,
                known_hosts_sha256=self.known_hosts_sha,
            )

    def test_bootstrap_mode_binds_the_unchanged_source_installer(self):
        receipt = self._receipt("bootstrap")
        self.assertIs(host_guest._validate_receipt(receipt, self.evidence), receipt)

        invalid = json.loads(json.dumps(receipt))
        invalid["identity"]["installerDriver"] = "nix-archive"
        with self.assertRaisesRegex(host_guest.HostGuestError, "install mode and driver"):
            host_guest._validate_receipt(invalid, self.evidence)

        self._write("guest-bootstrap-sha256.txt", ("sha256:" + "8" * 64 + "\n").encode())
        invalid = json.loads(json.dumps(receipt))
        invalid["evidenceInventory"] = qualification._qualification_evidence_inventory(self.evidence)
        with self.assertRaisesRegex(host_guest.HostGuestError, "exact source-tree installer"):
            host_guest._validate_receipt(invalid, self.evidence)


class FirstInstallGuestFixtureTests(unittest.TestCase):
    def test_probe_credentials_and_urls_are_private_and_role_specific(self):
        credentials = host_guest._postgres_fixture_credentials()
        self.assertEqual(set(credentials), set(host_guest.POSTGRES_PASSWORD_KEYS))
        self.assertEqual(len(set(credentials.values())), len(credentials))
        postgres_env = host_guest._postgres_fixture_environment(credentials).decode()
        for key in host_guest.POSTGRES_PASSWORD_KEYS.values():
            self.assertIn(key + "=", postgres_env)
        urls = host_guest._postgres_connection_urls(credentials)
        self.assertEqual(set(urls), {field for field, _, _, _ in host_guest.POSTGRES_URLS})
        for field, credential, database, role in host_guest.POSTGRES_URLS:
            parsed = urllib.parse.urlsplit(urls[field])
            self.assertEqual(parsed.scheme, "postgres")
            self.assertEqual(parsed.hostname, "postgres")
            self.assertEqual(parsed.port, 5432)
            self.assertEqual(parsed.username, role)
            self.assertEqual(parsed.password, credentials[credential])
            self.assertEqual(parsed.path, "/" + database)
            self.assertEqual(urllib.parse.parse_qs(parsed.query), {
                "sslmode": ["verify-full"],
                "sslrootcert": ["/var/lib/leapview/home/postgres-root.crt"],
            })

        template = (ROOT / "deploy/compose/leapview.env.example").read_bytes()
        probe = host_guest._pool_probe_environment(template, urls, {"adminEmail": "guest@example.test"})
        self.assertIn(b"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL=\n", probe)
        self.assertNotIn(credentials["controlMigrator"].encode(), probe)
        self.assertNotIn(credentials["duckLakeMigrator"].encode(), probe)
        self.assertNotIn(urls["controlMigratorUrl"].encode(), probe)
        self.assertNotIn(urls["duckLakeMigratorUrl"].encode(), probe)

    def test_operator_input_transports_only_image_generated_pool_artifacts(self):
        credentials = host_guest._postgres_fixture_credentials()
        urls = host_guest._postgres_connection_urls(credentials)
        generated = json.dumps({
            "schema_version": 1,
            "pool": {"pool_id": "image-produced"},
            "evidence": {"schema_version": 1, "evidence": {"checks": []}},
        }).encode() + b"\n"
        operator = json.loads(host_guest._qualification_operator_config(generated, urls))
        self.assertEqual(operator["schemaVersion"], 1)
        self.assertEqual(operator["postgres"], urls)
        self.assertEqual(operator["physicalPool"]["pool"], {"pool_id": "image-produced"})
        self.assertEqual(operator["physicalPool"]["evidence"], {"schema_version": 1, "evidence": {"checks": []}})
        self.assertNotIn("schema_version", operator)

        with self.assertRaisesRegex(host_guest.HostGuestError, "unsupported schema"):
            host_guest._qualification_operator_config(b'{"schema_version":1,"pool":{},"evidence":{},"extra":true}', urls)

    def test_source_locked_postgres_image_normalizes_to_docker_repo_digest(self):
        locked = POSTGRES_IMAGE
        self.assertEqual(
            host_guest._postgres_repo_digest(locked),
            "postgres@sha256:" + "8" * 64,
        )
        wrong = locked.rsplit("sha256:", 1)[0] + "sha256:" + "9" * 64
        self.assertNotEqual(host_guest._postgres_repo_digest(locked), host_guest._postgres_repo_digest(wrong))

    def test_guest_command_contract_prepares_pool_before_first_install(self):
        probe = host_guest._pool_qualification_command("/var/tmp/pool-probe", "DOCKER_CONFIG='/tmp/docker'")
        self.assertIn("run --rm --no-deps --no-TTY --entrypoint sh leapview", probe)
        self.assertIn("/usr/local/bin/leapview admin delivery pool qualify", probe)
        self.assertNotIn(" serve", probe)
        self.assertNotIn(" compose up", probe)
        self.assertIn("test -w /var/lib/leapview/home", probe)

        nix_install = host_guest._host_install_command(
            mode="nix-controller", docker_env="DOCKER_CONFIG=/tmp/docker", controller_path="/tmp/leapviewctl",
            config_path="/tmp/config.json", payload_path="/tmp/deployment", image=IMAGE,
        )
        self.assertIn("host install", nix_install)
        self.assertIn("--operator-config /run/leapview/operator-bootstrap.json", nix_install)
        bootstrap_install = host_guest._host_install_command(
            mode="bootstrap", docker_env="DOCKER_CONFIG=/tmp/docker", controller_path="/tmp/bootstrap.sh",
            config_path="/tmp/config.json", payload_path="/tmp/deployment", image=IMAGE,
        )
        self.assertIn("test -s /run/leapview/operator-bootstrap.json", bootstrap_install)
        self.assertIn("test ! -e /opt/leapview", bootstrap_install)
        self.assertIn("/tmp/bootstrap.sh install", bootstrap_install)
        self.assertNotIn("prepare-host", bootstrap_install)
        prepare = host_guest._bootstrap_prepare_command("/tmp/bootstrap.sh", "DOCKER_CONFIG=/tmp/docker")
        self.assertIn("/tmp/bootstrap.sh prepare-host", prepare)
        self.assertNotIn("install >/dev/null", prepare)

    def test_private_fixture_secrets_are_rejected_from_retained_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = Path(directory)
            (evidence / "safe.txt").write_text("safe evidence\n")
            host_guest._assert_no_secrets_in_evidence(evidence, ["private-url", "private-password"])
            (evidence / "unsafe.txt").write_text("copied private-password by mistake")
            with self.assertRaisesRegex(host_guest.HostGuestError, "credential appeared"):
                host_guest._assert_no_secrets_in_evidence(evidence, ["private-url", "private-password"])

if __name__ == "__main__":
    unittest.main()
