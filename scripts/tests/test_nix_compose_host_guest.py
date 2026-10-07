import json
import subprocess
from pathlib import Path
import sys
import urllib.parse
import tempfile
import unittest
from unittest.mock import patch

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


def _swap_phase(report, first, second):
    report["phases"][first], report["phases"][second] = report["phases"][second], report["phases"][first]


class HostGuestReceiptTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.evidence = self.root / "evidence"
        self.evidence.mkdir()
        self.image_id = "sha256:" + "e" * 64
        self.current_target = "releases/sha256-" + "a" * 64
        self.host_marker_private = {
            "schemaVersion": 1, "domain": "localhost", "adminEmail": "qualification@example.test",
            "environment": "prod", "https": True, "image": IMAGE,
            "targetId": "nix-host-qualification", "bootstrapPhase": "private-bootstrap",
            "generation": self.current_target.removeprefix("releases/"),
        }
        self.marker_sha = host_guest._digest(host_guest._canonical(self.host_marker_private) + b"\n")
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
        self._write("host-marker-private-after-install.json", host_guest._canonical(self.host_marker_private) + b"\n")
        self._write("host-marker-sha256.txt", (self.marker_sha + "\n").encode())
        self._write("host-marker-projection.json", json.dumps({
            **{key: value for key, value in self.host_marker_private.items()}, "markerSHA256": self.marker_sha,
        }, sort_keys=True).encode() + b"\n")
        self._write("docker-inspect-placeholder.txt", b"unused\n")
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
            "startedAtAfterPrivateReboot": "2026-10-04T12:05:00.000000000Z",
            "startedAtAfterReboot": "2026-10-04T12:15:00.000000000Z",
        }
        self.inspect_before = " ".join([
            self.container["id"], self.container["configuredImage"], self.container["imageID"],
            "running", "unhealthy", "leapview", "leapview", self.container["startedAtBeforeReboot"],
        ])
        self.inspect_before_publication = " ".join([
            self.container["id"], self.container["configuredImage"], self.container["imageID"],
            "running", "unhealthy", "leapview", "leapview", self.container["startedAtAfterPrivateReboot"],
        ])
        self.inspect_after = " ".join([
            self.container["id"], self.container["configuredImage"], self.container["imageID"],
            "running", "healthy", "leapview", "leapview", self.container["startedAtAfterReboot"],
        ])
        self._write("prereboot-container-inspect.txt", (self.inspect_before + "\n").encode())
        self._write("postreboot-before-publication-container-inspect.txt", (self.inspect_before_publication + "\n").encode())
        self._write("container-inspect.txt", (self.inspect_after + "\n").encode())
        self._write("post-public-reboot-container-inspect.txt", (self.inspect_after + "\n").encode())
        self._write("automatic-restart-probes.json", json.dumps([
            {"elapsedSeconds": 0.0, "inspection": self.inspect_before_publication},
            {"elapsedSeconds": 10.0, "inspection": self.inspect_after},
        ]).encode() + b"\n")
        self._write("controller-status.txt", b"0\n")

    def tearDown(self):
        self.temporary.cleanup()

    def _write(self, name, data):
        (self.evidence / name).write_bytes(data)

    def _first_publication_report(self):
        source_revision = "sha256:" + "b" * 64
        source_artifact = "sha256:" + "c" * 64
        release_digest = "sha256:" + "d" * 64
        plan_digest = "sha256:" + "e" * 64
        return {
            "schemaVersion": 1,
            "scope": host_guest.FIRST_PUBLICATION_SCOPE,
            "result": "passed",
            "request": {
                "targetURL": "https://localhost",
                "projectID": host_guest.FIRST_PUBLICATION_PROJECT_ID,
                "environment": "prod",
                "image": IMAGE,
                "imageSourceRevision": REVISION,
                "sourceRevision": source_revision,
                "candidateID": "candidate-1",
                "candidateRevision": 1,
                "targetID": "target-1",
                "principalID": "publisher-principal",
                "artifactDigest": source_artifact,
                "releaseDigest": release_digest,
                "planID": "plan-1",
                "planDigest": plan_digest,
            },
            "publication": {
                "candidateID": "candidate-1",
                "candidateRevision": 1,
                "targetID": "target-1",
                "publicationID": "publication-1",
                "publicationStatus": "committed",
                "generationID": "generation-1",
                "principalID": "publisher-principal",
                "sourceArtifactDigest": source_artifact,
                "servingArtifactDigest": "sha256:" + "f" * 64,
                "releaseDigest": release_digest,
                "sourceRevision": source_revision,
                "planID": "plan-1",
                "planDigest": plan_digest,
            },
            "approval": {
                "id": "approval-1",
                "status": "approved",
                "approvedBy": "reviewer-principal",
                "deploymentId": "publication-1",
                "projectId": host_guest.FIRST_PUBLICATION_PROJECT_ID,
                "environment": "prod",
                "requestDigest": "sha256:" + "9" * 64,
            },
            "publisherPrincipalID": "publisher-principal",
            "reviewerPrincipalID": "reviewer-principal",
            "readinessBefore": 503,
            "readinessAfter": 200,
            "phases": [
                {"name": name, "result": "success", "startedAt": "2026-10-05T12:00:00Z",
                 "durationMillis": 10, "timeoutSeconds": 60, "cleanupGuaranteed": True}
                for name in (
                    "browser and client setup", "reviewer provisioning", "native keyring login",
                    "private candidate preview", "protected publish",
                )
            ],
            "assertions": {
                "firstLoginConsumedOnce": True,
                "readinessTransitionObserved": True,
                "temporaryCredentialsRemoved": True,
                "secretsExcludedFromEvidence": True,
            },
        }

    def _write_first_publication_evidence(self):
        first_publication = self._first_publication_report()
        self._write("first-publication-report.json", host_guest._canonical(first_publication) + b"\n")
        self._write("first-publication-https-readiness-before.txt", b"503\n")
        self._write("first-publication-https-readiness-after.txt", b"200\n")
        verifier = {
            "protectedRevision": "2" * 40,
            "verifierSHA256": "sha256:" + "1" * 64,
            "qualificationAssetsSHA256": "sha256:" + "3" * 64,
        }
        self._write("protected-first-publication-source-revision.txt", (verifier["protectedRevision"] + "\n").encode())
        self._write("protected-first-publication-verifier-sha256.txt", (verifier["verifierSHA256"] + "\n").encode())
        assets_manifest = {
            "schemaVersion": 1,
            "assets": {name: "sha256:" + str(index) * 64 for index, name in enumerate(
                host_guest.PROTECTED_QUALIFICATION_ASSETS, start=4,
            )},
        }
        verifier["qualificationAssetsSHA256"] = host_guest._digest(host_guest._canonical(assets_manifest))
        self._write(
            "protected-qualification-assets-sha256.txt",
            (verifier["qualificationAssetsSHA256"] + "\n").encode(),
        )
        self._write(
            "protected-qualification-assets-manifest.json",
            host_guest._canonical(assets_manifest) + b"\n",
        )
        return first_publication, verifier

    def _write_first_install_lifecycle_evidence(self):
        target_id = "nix-host-qualification"
        private = dict(self.host_marker_private)
        public = {**private, "bootstrapPhase": "public"}
        marker_records = {
            "privateAfterInstall": ("afterInstall", "host-marker-private-after-install.json", private),
            "privateAfterPendingReboot": ("afterPendingReboot", "host-marker-private-after-reboot.json", private),
            "privateAfterPublication": ("afterPublication", "host-marker-private-after-publication.json", private),
            "publicAfterActivation": ("afterActivation", "host-marker-public-after-activation.json", public),
            "publicAfterReboot": ("afterPublicReboot", "host-marker-public-after-reboot.json", public),
        }
        marker_hashes = {}
        for digest_name, (_, filename, marker) in marker_records.items():
            raw = host_guest._canonical(marker) + b"\n"
            marker_hashes[digest_name] = host_guest._digest(raw)
            self._write(filename, raw)
            stem = filename.removesuffix(".json")
            self._write(stem + "-current-link.txt", ("releases/" + marker["generation"] + "\n").encode())
            self._write(stem + "-owner-mode.txt", b"0:0:600\n")
        marker_hashes["privateAfterInstall"] = self.marker_sha
        self._write("host-marker-sha256.txt", (self.marker_sha + "\n").encode())
        self._write("host-marker-projection.json", host_guest._canonical({
            **{key: private[key] for key in (
                "schemaVersion", "image", "domain", "environment", "https", "targetId",
                "bootstrapPhase", "generation",
            )},
            "markerSHA256": self.marker_sha,
        }) + b"\n")

        private_raw_bindings = {
            "80/tcp": [{"HostIp": "127.0.0.1", "HostPort": "80"}],
            "443/tcp": [{"HostIp": "127.0.0.1", "HostPort": "443"}],
            "443/udp": [{"HostIp": "127.0.0.1", "HostPort": "443"}],
        }
        public_raw_bindings = {
            key: [{"HostIp": "0.0.0.0", "HostPort": entries[0]["HostPort"]}]
            for key, entries in private_raw_bindings.items()
        }
        private_caddyfile = b"{$CADDY_DOMAIN} {\n  tls internal\n  encode zstd gzip\n  reverse_proxy leapview:8080\n}\n"
        public_caddyfile = b"{$CADDY_DOMAIN} {\n  encode zstd gzip\n  reverse_proxy leapview:8080\n}\n"
        caddy_image = "caddy:2.10.2-alpine@sha256:" + "7" * 64
        caddy_specs = (
            ("private-caddy-before-reboot", private_raw_bindings, private_caddyfile, "8" * 64,
             "2026-10-04T12:00:00.000000000Z", True),
            ("private-caddy-after-reboot", private_raw_bindings, private_caddyfile, "8" * 64,
             "2026-10-04T12:05:00.000000000Z", True),
            ("private-caddy-after-publication", private_raw_bindings, private_caddyfile, "8" * 64,
             "2026-10-04T12:05:00.000000000Z", True),
            ("public-caddy-after-activation", public_raw_bindings, public_caddyfile, "9" * 64,
             "2026-10-04T12:10:00.000000000Z", False),
            ("public-caddy-after-reboot", public_raw_bindings, public_caddyfile, "9" * 64,
             "2026-10-04T12:15:00.000000000Z", False),
        )
        caddy_observations = {}
        for prefix, raw_bindings, caddyfile, container_id, started_at, is_private in caddy_specs:
            inspection = {
                "id": container_id,
                "image": caddy_image,
                "status": "running",
                "project": "leapview",
                "service": "caddy",
                "startedAt": started_at,
            }
            raw_ports = json.dumps(raw_bindings, sort_keys=True, separators=(",", ":"))
            self._write(prefix + "-docker-inspect.txt", (
                "|".join((container_id, caddy_image, "running", "leapview", "caddy", started_at, raw_ports)) + "\n"
            ).encode())
            self._write(prefix + "-active-caddyfile.txt", caddyfile)
            self._write(prefix + "-active-domain.txt", b"localhost\n")
            caddy_observations[prefix] = {
                "inspection": inspection,
                "portBindings": host_guest._validate_caddy_port_bindings(
                    raw_bindings, private=is_private,
                    public_config=None if is_private else {
                        "CADDY_HTTP_BIND": "80", "CADDY_HTTPS_BIND": "443", "CADDY_HTTPS_UDP_BIND": "443",
                    },
                ),
                "caddyfileSHA256": host_guest._digest(caddyfile),
                "domain": "localhost",
            }

        bind_config = {
            "CADDY_HTTP_BIND": "80", "CADDY_HTTPS_BIND": "443", "CADDY_HTTPS_UDP_BIND": "443",
        }
        self._write("public-caddy-bind-config.txt", b"CADDY_HTTP_BIND=80\nCADDY_HTTPS_BIND=443\nCADDY_HTTPS_UDP_BIND=443\n")
        raw_app_bindings = {"8080/tcp": [{"HostIp": "127.0.0.1", "HostPort": "8080"}]}
        app_normalized = host_guest._validate_application_port_bindings(raw_app_bindings)
        app_binding_records = {
            "beforePendingReboot": "application-port-bindings-before-reboot.json",
            "afterPendingReboot": "application-port-bindings-after-private-reboot.json",
            "afterPublication": "application-port-bindings-after-publication.json",
            "afterActivation": "application-port-bindings-after-activation.json",
            "afterPublicReboot": "application-port-bindings-after-public-reboot.json",
        }
        for filename in app_binding_records.values():
            self._write(filename, host_guest._canonical(raw_app_bindings) + b"\n")

        for filename, status in (
            ("private-bootstrap-healthz-before-reboot.txt", "200"),
            ("private-bootstrap-readyz-before-reboot.txt", "503"),
            ("private-bootstrap-healthz-after-reboot.txt", "200"),
            ("private-bootstrap-readyz-after-reboot.txt", "503"),
            ("first-publication-direct-readyz-after.txt", "200"),
            ("public-activation-healthz.txt", "200"),
            ("public-activation-readyz.txt", "200"),
            ("public-reboot-healthz.txt", "200"),
            ("public-reboot-readyz.txt", "200"),
            ("public-activation-https-readiness.txt", "200"),
            ("public-reboot-https-readiness.txt", "200"),
        ):
            self._write(filename, (status + "\n").encode())
        self._write("first-install-activation-exit-code.txt", b"0\n")
        public_boot = "33333333-3333-4333-8333-333333333333"
        self._write("post-public-reboot-boot-id.txt", (public_boot + "\n").encode())
        self._write("application-port-bindings-after-public-reboot.json", host_guest._canonical(raw_app_bindings) + b"\n")
        return {
            "targetID": target_id,
            "privateMarkers": {phase_name: value for digest_name, (phase_name, _, value) in marker_records.items()
                               if digest_name.startswith("private")},
            "publicMarkers": {phase_name: value for digest_name, (phase_name, _, value) in marker_records.items()
                              if digest_name.startswith("public")},
            "markerSHA256": marker_hashes,
            "privateCaddy": {
                "beforePendingReboot": caddy_observations["private-caddy-before-reboot"],
                "afterPendingReboot": caddy_observations["private-caddy-after-reboot"],
                "afterPublication": caddy_observations["private-caddy-after-publication"],
            },
            "publicCaddy": {
                "afterActivation": caddy_observations["public-caddy-after-activation"],
                "afterPublicReboot": caddy_observations["public-caddy-after-reboot"],
            },
            "publicBindConfig": bind_config,
            "applicationPortBindings": {name: app_normalized for name in app_binding_records},
            "bootIDAfterPendingReboot": BOOT_AFTER,
            "bootIDAfterPublicReboot": public_boot,
            "activationExitCode": 0,
        }

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
        first_publication, protected_verifier = self._write_first_publication_evidence()
        first_install_lifecycle = self._write_first_install_lifecycle_evidence()
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
            "firstPublication": {
                "report": first_publication,
                "httpsReadiness": {"url": "https://localhost/readyz", "before": 503, "after": 200},
            },
            "firstInstallLifecycle": first_install_lifecycle,
            "protectedVerifier": protected_verifier,
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

    def test_first_publication_subreport_binds_request_commit_reviewer_and_readyz(self):
        report = self._first_publication_report()
        self.assertIs(host_guest._validate_first_publication_report(
            report, image=IMAGE, image_revision=REVISION, environment="prod",
        ), report)

        invalid_cases = {
            "public target": lambda value: value["request"].update(targetURL="https://example.com"),
            "wrong image": lambda value: value["request"].update(image="ghcr.io/flidai/leapview:latest"),
            "git and managed revision conflated": lambda value: (
                value["request"].update(sourceRevision=REVISION),
                value["publication"].update(sourceRevision=REVISION),
            ),
            "source and serving artifact conflated": lambda value: value["publication"].update(
                servingArtifactDigest=value["request"]["artifactDigest"],
            ),
            "candidate mismatch": lambda value: value["publication"].update(candidateID="candidate-other"),
            "publication mismatch": lambda value: value["approval"].update(deploymentId="publication-other"),
            "reviewer is publisher": lambda value: value["approval"].update(approvedBy=value["publisherPrincipalID"]),
            "reviewer ordered after preview": lambda value: _swap_phase(value, 1, 3),
            "phase cleanup is not guaranteed": lambda value: value["phases"][0].update(cleanupGuaranteed=False),
            "readiness did not transition": lambda value: value.update(readinessAfter=503),
            "credential assertion failed": lambda value: value["assertions"].update(secretsExcludedFromEvidence=False),
            "unexpected credential field": lambda value: value.update(rawCredentials={"password": "should-not-be-retained"}),
        }
        for name, mutate in invalid_cases.items():
            with self.subTest(name=name):
                invalid = json.loads(json.dumps(report))
                mutate(invalid)
                with self.assertRaises(host_guest.HostGuestError):
                    host_guest._validate_first_publication_report(
                        invalid, image=IMAGE, image_revision=REVISION, environment="prod",
                    )

    def test_first_publication_receipt_binds_protected_helper_and_https_observations(self):
        receipt = self._receipt()
        self.assertIs(host_guest._validate_receipt(receipt, self.evidence), receipt)

        invalid = json.loads(json.dumps(receipt))
        invalid["protectedVerifier"]["protectedRevision"] = REVISION
        with self.assertRaisesRegex(host_guest.HostGuestError, "protected verifier binding"):
            host_guest._validate_receipt(invalid, self.evidence)

        invalid = json.loads(json.dumps(receipt))
        invalid["firstPublication"]["httpsReadiness"]["before"] = 200
        with self.assertRaisesRegex(host_guest.HostGuestError, "independent HTTPS readiness"):
            host_guest._validate_receipt(invalid, self.evidence)

        invalid = json.loads(json.dumps(receipt))
        invalid["firstPublication"]["report"]["reviewerPrincipalID"] = "publisher-principal"
        with self.assertRaisesRegex(host_guest.HostGuestError, "independent reviewer"):
            host_guest._validate_receipt(invalid, self.evidence)

    def test_first_publication_profile_requires_private_localhost_https_prod(self):
        valid = {"domain": "localhost", "https": True, "environment": "prod"}
        self.assertIsNone(host_guest._validate_first_publication_host_config(valid))
        for invalid in (
            {**valid, "domain": "public.example"},
            {**valid, "https": False},
            {**valid, "environment": "evaluation"},
        ):
            with self.subTest(invalid=invalid), self.assertRaises(host_guest.HostGuestError):
                host_guest._validate_first_publication_host_config(invalid)

    def test_host_install_marker_binds_private_and_public_phases_to_same_generation(self):
        marker = {
            "schemaVersion": 1,
            "domain": "localhost",
            "adminEmail": "qualification@example.test",
            "environment": "prod",
            "https": True,
            "image": IMAGE,
            "targetId": "nix-host-qualification",
            "bootstrapPhase": "private-bootstrap",
            "generation": host_guest._first_install_generation(IMAGE),
        }
        host_guest._validate_host_install_marker(
            marker, phase="private-bootstrap", image=IMAGE, target_id="nix-host-qualification",
        )
        public_marker = {**marker, "bootstrapPhase": "public"}
        host_guest._validate_host_install_marker(
            public_marker, phase="public", image=IMAGE, target_id="nix-host-qualification",
        )
        for invalid in (
            {**marker, "bootstrapPhase": "public"},
            {**marker, "generation": "sha256-" + "b" * 64},
            {**marker, "image": "ghcr.io/flidai/leapview:latest"},
            {**marker, "extra": "untrusted"},
        ):
            with self.subTest(invalid=invalid), self.assertRaises(host_guest.HostGuestError):
                host_guest._validate_host_install_marker(
                    invalid, phase="private-bootstrap", image=IMAGE, target_id="nix-host-qualification",
                )

    def test_first_install_lifecycle_receipt_rejects_phase_and_listener_tampering(self):
        receipt = self._receipt()
        lifecycle = receipt["firstInstallLifecycle"]
        validate = lambda value: host_guest._validate_first_install_lifecycle(
            value, self.evidence, image=IMAGE, boot_id_before=BOOT_BEFORE,
            boot_id_after_pending_reboot=BOOT_AFTER,
        )
        self.assertIs(validate(lifecycle), lifecycle)

        wrong_generation = json.loads(json.dumps(lifecycle))
        wrong_generation["publicMarkers"]["afterActivation"]["generation"] = "sha256-" + "b" * 64
        with self.assertRaisesRegex(host_guest.HostGuestError, "expected phase"):
            validate(wrong_generation)

        private_marker_path = self.evidence / "host-marker-private-after-reboot.json"
        original_private_marker = private_marker_path.read_bytes()
        leaked_public_phase = json.loads(original_private_marker)
        leaked_public_phase["bootstrapPhase"] = "public"
        private_marker_path.write_bytes(host_guest._canonical(leaked_public_phase) + b"\n")
        with self.assertRaisesRegex(host_guest.HostGuestError, "retained installation marker"):
            validate(lifecycle)
        private_marker_path.write_bytes(original_private_marker)

        public_inspection_path = self.evidence / "public-caddy-after-activation-docker-inspect.txt"
        original_public_inspection = public_inspection_path.read_bytes()
        fields = original_public_inspection.decode("utf-8").strip().split("|", 6)
        public_bindings = json.loads(fields[6])
        public_bindings["443/tcp"][0]["HostIp"] = "127.0.0.1"
        fields[6] = json.dumps(public_bindings, sort_keys=True, separators=(",", ":"))
        public_inspection_path.write_text("|".join(fields) + "\n")
        with self.assertRaisesRegex(host_guest.HostGuestError, "loopback"):
            validate(lifecycle)
        public_inspection_path.write_bytes(original_public_inspection)

        public_app_path = self.evidence / "application-port-bindings-after-public-reboot.json"
        original_app_bindings = public_app_path.read_bytes()
        exposed_app = {"8080/tcp": [{"HostIp": "0.0.0.0", "HostPort": "8080"}]}
        public_app_path.write_bytes(host_guest._canonical(exposed_app) + b"\n")
        with self.assertRaisesRegex(host_guest.HostGuestError, "application listener"):
            validate(lifecycle)
        public_app_path.write_bytes(original_app_bindings)

    def test_caddy_inspection_proves_private_loopback_and_public_port_bindings(self):
        inspection = {
            "id": "4" * 64,
            "image": "caddy:2.10.2-alpine@sha256:" + "8" * 64,
            "status": "running",
            "project": "leapview",
            "service": "caddy",
            "startedAt": "2026-10-05T12:00:00Z",
        }
        host_guest._validate_caddy_inspection(inspection)
        private = {
            "80/tcp": [{"HostIp": "127.0.0.1", "HostPort": "80"}],
            "443/tcp": [{"HostIp": "127.0.0.1", "HostPort": "443"}],
            "443/udp": [{"HostIp": "127.0.0.1", "HostPort": "443"}],
        }
        normalized_private = host_guest._validate_caddy_port_bindings(private, private=True)
        self.assertEqual(normalized_private["443/tcp"]["hostIP"], "127.0.0.1")
        public = {key: [{"HostIp": "0.0.0.0", "HostPort": entries[0]["HostPort"]}]
                  for key, entries in private.items()}
        public_config = host_guest._parse_caddy_bind_config(
            b"CADDY_HTTP_BIND=80\nCADDY_HTTPS_BIND=443\nCADDY_HTTPS_UDP_BIND=443\n",
        )
        normalized_public = host_guest._validate_caddy_port_bindings(
            public, private=False, public_config=public_config,
        )
        self.assertEqual(normalized_public["443/tcp"]["hostIP"], "0.0.0.0")
        with self.assertRaisesRegex(host_guest.HostGuestError, "loopback"):
            host_guest._validate_caddy_port_bindings(private, private=False)
        exposed_private = {**private, "443/tcp": [{"HostIp": "0.0.0.0", "HostPort": "443"}]}
        with self.assertRaisesRegex(host_guest.HostGuestError, "beyond IPv4 loopback"):
            host_guest._validate_caddy_port_bindings(exposed_private, private=True)
        missing_udp = {key: value for key, value in private.items() if key != "443/udp"}
        with self.assertRaisesRegex(host_guest.HostGuestError, "exact Compose listener set"):
            host_guest._validate_caddy_port_bindings(missing_udp, private=True)
        wrong_port = {**public, "443/tcp": [{"HostIp": "0.0.0.0", "HostPort": "444"}]}
        with self.assertRaisesRegex(host_guest.HostGuestError, "unexpected host port"):
            host_guest._validate_caddy_port_bindings(wrong_port, private=False, public_config=public_config)
        with self.assertRaisesRegex(host_guest.HostGuestError, "loopback"):
            host_guest._validate_application_port_bindings({
                "8080/tcp": [{"HostIp": "0.0.0.0", "HostPort": "8080"}],
            })
        self.assertEqual(host_guest._validate_application_port_bindings({
            "8080/tcp": [{"HostIp": "127.0.0.1", "HostPort": "8080"}],
        }), {"8080/tcp": {"hostIP": "127.0.0.1", "hostPort": "8080"}})

    def test_caddy_observation_retry_does_not_retain_partial_evidence(self):
        bindings = {key: [{"HostIp": "0.0.0.0", "HostPort": port}]
                    for key, port in (("80/tcp", "80"), ("443/tcp", "443"), ("443/udp", "443"))}
        inspection = ("|".join(("4" * 64, "caddy:pinned", "running", "leapview", "caddy",
                               "2026-10-05T12:00:00Z", json.dumps(bindings))) + "\n").encode()
        config = b"{$CADDY_DOMAIN} {\n  reverse_proxy leapview:8080\n}\n"

        class Guest:
            fail_domain = True

            def run(self, command, **kwargs):
                if "docker inspect" in command:
                    return inspection
                if "cat /etc/caddy/Caddyfile" in command:
                    return config
                if "printenv CADDY_DOMAIN" in command:
                    if self.fail_domain:
                        self.fail_domain = False
                        raise host_guest.HostGuestError("container temporarily unavailable")
                    return b"localhost\n"
                raise AssertionError(command)

        with patch.object(host_guest.time, "sleep"):
            observation = host_guest._wait_for_caddy_observation(
                Guest(), self.evidence, docker_env="", prefix="retry-caddy",
                private=False, timeout=5,
            )
        self.assertEqual(observation["domain"], "localhost")
        self.assertEqual((self.evidence / "retry-caddy-docker-inspect.txt").read_bytes(), inspection)

    def test_active_caddyfile_records_private_internal_tls_transition(self):
        private = b"{$CADDY_DOMAIN} {\n  tls internal\n  reverse_proxy leapview:8080\n}\n"
        public = b"{$CADDY_DOMAIN} {\n  encode zstd gzip\n  reverse_proxy leapview:8080\n}\n"
        self.assertEqual(host_guest._validate_active_caddyfile(private, private=True), private.decode())
        self.assertEqual(host_guest._validate_active_caddyfile(public, private=False), public.decode())
        with self.assertRaisesRegex(host_guest.HostGuestError, "TLS phase"):
            host_guest._validate_active_caddyfile(public, private=True)
        with self.assertRaisesRegex(host_guest.HostGuestError, "TLS phase"):
            host_guest._validate_active_caddyfile(private, private=False)

    def test_protected_qualification_assets_are_exact_regular_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            asset_root = root / "deploy/compose/qualification"
            asset_root.mkdir(parents=True)
            for name in host_guest.PROTECTED_QUALIFICATION_ASSETS:
                (asset_root / name).write_text("protected " + name + "\n")
            assets, manifest, digest = host_guest._protected_qualification_assets(root)
            self.assertEqual(set(assets), set(host_guest.PROTECTED_QUALIFICATION_ASSETS))
            self.assertEqual(set(manifest["assets"]), set(host_guest.PROTECTED_QUALIFICATION_ASSETS))
            self.assertEqual(digest, host_guest._digest(host_guest._canonical(manifest)))
            (asset_root / "authoring-worker.mjs").unlink()
            (asset_root / "authoring-worker.mjs").symlink_to(asset_root / "package.json")
            with self.assertRaisesRegex(host_guest.HostGuestError, "regular file"):
                host_guest._protected_qualification_assets(root)

    def test_https_readiness_probe_is_loopback_tls_and_retains_only_status(self):
        command = host_guest._https_readiness_command()
        self.assertIn("openssl s_client", command)
        self.assertIn("127.0.0.1:443", command)
        self.assertIn("-servername localhost", command)
        self.assertIn("GET /readyz HTTP/1.1", command)
        self.assertIn("200|503", command)
        self.assertNotIn("curl", command)


class FirstInstallGuestFixtureTests(unittest.TestCase):
    def test_prerequisite_failure_retains_bounded_diagnostics_and_stops_setup(self):
        commands = []

        class Guest:
            def run(self, command, **kwargs):
                commands.append(command)
                if "apt-get install" in command:
                    return b"100\nE: Not enough free space in /var/cache/apt/archives/\n"
                return b"0\npackage index updated\n"

        with tempfile.TemporaryDirectory() as temporary:
            evidence = Path(temporary)
            with self.assertRaisesRegex(host_guest.HostGuestError, "apt-install failed \\(100\\)"):
                host_guest._guest_package_setup(Guest(), "debian13", evidence=evidence)
            self.assertEqual(len(commands), 2)
            self.assertIn(b"Not enough free space", (evidence / "prerequisite-apt-install.log").read_bytes())
            self.assertEqual((evidence / "prerequisite-apt-install-exit-code.txt").read_bytes(), b"100\n")

    def test_prerequisite_probe_keeps_exit_status_and_bounds_output(self):
        class Guest:
            def run(self, command, **kwargs):
                return subprocess.run(["bash", "-c", command], check=True, capture_output=True).stdout

        with tempfile.TemporaryDirectory() as temporary:
            evidence = Path(temporary)
            with self.assertRaisesRegex(host_guest.HostGuestError, "fixture failed \\(7\\)"):
                host_guest._guest_prerequisite(
                    Guest(), evidence, "fixture", "printf 'failure on stderr\\n' >&2; exit 7", timeout=10,
                )
            self.assertEqual((evidence / "prerequisite-fixture.log").read_bytes(), b"failure on stderr\n")
            host_guest._guest_prerequisite(
                Guest(), evidence, "large", "head -c 100000 /dev/zero", timeout=10,
            )
            self.assertEqual((evidence / "prerequisite-large.log").stat().st_size, 65536)

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
        self.assertEqual(operator["postgresProfile"], "external")
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
