#!/usr/bin/env python3
"""Real admitted application lifecycle in disposable, offline namespaces.

This is development lifecycle evidence, never full managed-profile admission.
The parent imports authentic producer receipts and stages original image stores;
the child cannot reach a registry and never manufactures an admission record.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time

sys.dont_write_bytecode = True
SOURCE = Path(__file__).resolve().parents[4]
sys.path.insert(0, str(SOURCE / "scripts"))
import managed_application_inputs as inputs
import nix_compose_host_guest as guest
from docker_daemon_test import namespace_ids, verify_namespaces
from managed_application_support import prepare_postgres

HIDDEN = tuple(Path(path) for path in ("/root", "/run", "/var", "/opt", "/etc", "/usr"))


def validate_visible_paths(paths):
    for path in paths:
        if any(Path(path).resolve().is_relative_to(hidden) for hidden in HIDDEN):
            raise ValueError("qualification inputs must remain outside hidden host roots")


def verify_retained(expected, actual):
    if (actual.get("Id") != expected["imageID"] or expected.get("repositoryDigest", expected["image"]) not in actual.get("RepoDigests", [])
            or actual.get("Os", "") + "/" + actual.get("Architecture", "") != expected["platform"]):
        raise ValueError("offline image inventory differs from authenticated staged identity")


def release_request(first, second, first_projection, second_projection, source, *, enrollment=False):
    if first_projection != second_projection:
        raise ValueError("qualification cannot change runtime configuration or credentials")
    if enrollment and first != second:
        raise ValueError("enrollment must select the same exact admitted release")
    def release(selection):
        return {"image": selection["image"], "revision": selection["sourceRevision"],
                "artifactAdmissionDigest": selection["admissionDigest"], **first_projection}
    return {"version": 1, "target": "managed-application-qualification",
            "operation": "enroll" if enrollment else "",
            "predecessor": release(first), "candidate": release(second),
            "sourceBefore": source, "sourceAfter": source,
            "budgets": {"phase": 180 * 10**9, "total": 1200 * 10**9}}


def private_file(path, value, mode=0o600):
    path = Path(path)
    with path.open("x") as stream:
        os.fchmod(stream.fileno(), mode)
        stream.write(value if isinstance(value, str) else json.dumps(value, sort_keys=True) + "\n")
    return path


def run(*argv, timeout=120, check=True, **kwargs):
    result = subprocess.run([str(arg) for arg in argv], capture_output=True, text=True, timeout=timeout, **kwargs)
    if check and result.returncode:
        # Commands may contain private URLs or print one-time credentials.
        raise RuntimeError("qualification command failed: " + Path(argv[0]).name)
    return result


def wait(action, timeout=60):
    deadline = time.monotonic() + timeout
    while not action():
        if time.monotonic() >= deadline:
            raise RuntimeError("qualification readiness deadline exceeded")
        time.sleep(.2)


def stage(evidence, name):
    evidence["stage"] = name
    print("managed application: " + name, flush=True)


class Runtime:
    def __init__(self, args, manifest, staged, evidence):
        self.args, self.manifest, self.staged, self.evidence = args, manifest, staged, evidence
        self.processes = []
        self.docker_bin = str(args.docker_package.resolve() / "bin/docker")
        self.dockerd_bin = str(args.docker_package.resolve() / "bin/dockerd")
        self.controller = str(args.controller.resolve())
        self.root = Path("/root/managed")
        self.profile_path = Path("/root/profile.json")
        self.state = Path("/var/lib/leapviewctl")

    def launch(self, argv, name, **kwargs):
        output = open("/root/" + name + ".log", "x")
        os.fchmod(output.fileno(), 0o600)
        process = subprocess.Popen([str(arg) for arg in argv], stdout=output, stderr=output, **kwargs)
        self.processes.append((process, output))
        return process

    def docker(self, *args, **kwargs):
        return run(self.docker_bin, "--host", "unix:///var/run/docker.sock", *args, **kwargs)

    def support_docker(self, *args, **kwargs):
        return run(self.docker_bin, "--host", "unix:///run/support-docker.sock", *args, **kwargs)

    def prepare(self):
        verify_namespaces(self.args.namespace_parent)
        if {item["ifname"] for item in json.loads(run("ip", "-j", "link", "show").stdout)} != {"lo"}:
            raise RuntimeError("qualification requires an empty isolated network")
        passwd = Path("/etc/passwd").read_text()
        group = Path("/etc/group").read_text()
        system = Path("/run/current-system").resolve()
        path = str(self.args.tools.resolve() / "bin") + ":" + str(self.args.docker_package.resolve() / "bin")
        path += ":" + ":".join(str(Path(entry).resolve()) for entry in os.environ["PATH"].split(":"))
        os.environ["PATH"] = path
        run("mount", "--make-rprivate", "/")
        for hidden in HIDDEN:
            run("mount", "-t", "tmpfs", "tmpfs", hidden)
            hidden.chmod(0o755)
        Path("/root").chmod(0o700)
        for directory in ("/var/empty", "/var/lib", "/run/netns", "/opt/leapview-fixture", "/usr/bin", "/etc/ssl/certs"):
            Path(directory).mkdir(parents=True, mode=0o755)
        for name in ("sh", "bash", "env"):
            Path("/usr/bin", name).symlink_to(self.args.tools.resolve() / "bin" / ("bash" if name == "sh" else name))
        Path("/var/run").symlink_to("/run")
        if str(system).startswith("/nix/store/"):
            Path("/run/current-system").symlink_to(system)
        os.environ.clear()
        os.environ.update(PATH=path, HOME="/root", TMPDIR="/root", XDG_RUNTIME_DIR="/run",
                          DOCKER_HOST="unix:///var/run/docker.sock", LEAPVIEWCTL_ROOT="/opt/leapview")
        private_file("/etc/passwd", "\n".join(
            "root::0:0:qualification:/root:" + str(self.args.tools.resolve() / "bin/bash")
            if line.startswith("root:") else line for line in passwd.splitlines()) + "\n", 0o644)
        private_file("/etc/shadow", "root::1:0:99999:7:::\n")
        private_file("/etc/group", group, 0o644)
        private_file("/etc/nsswitch.conf", "passwd: files\ngroup: files\nhosts: files dns\n", 0o644)
        private_file("/etc/hosts", "127.0.0.1 localhost\n::1 localhost\n", 0o644)
        private_file("/etc/resolv.conf", "nameserver 172.30.0.1\noptions timeout:1 attempts:1\n", 0o644)
        run("ip", "link", "set", "lo", "up")
        run("ip", "netns", "add", "public")
        run("ip", "link", "add", "eth-public", "type", "veth", "peer", "name", "client")
        run("ip", "link", "set", "client", "netns", "public")
        run("ip", "addr", "add", "198.18.0.1/24", "dev", "eth-public")
        run("ip", "link", "set", "eth-public", "up")
        for argv in (("addr", "add", "198.18.0.2/24", "dev", "client"), ("link", "set", "client", "up"), ("link", "set", "lo", "up")):
            run("ip", "netns", "exec", "public", "ip", *argv)
        run("bash", SOURCE / "deploy/managed/nixos/modules/docker-firewall.sh", "eth-public")
        self.start_daemons()
        for name, expected in self.staged["images"].items():
            docker = self.support_docker if name == "postgres" else self.docker
            verify_retained(expected, json.loads(docker("image", "inspect", expected["image"]).stdout)[0])
        self.evidence["retainedImageIdentityVerified"] = True
        self.docker("network", "create", "--subnet", "172.30.0.0/24", "--gateway", "172.30.0.1", "kamal")
        self.launch(["dnsmasq", "--keep-in-foreground", "--conf-file=/dev/null", "--no-resolv", "--no-hosts",
                     "--bind-interfaces", "--listen-address=172.30.0.1", "--address=/postgres/172.30.0.1",
                     "--local=/postgres/", "--user=root"], "dns")
        Path("/var/lib/leapview").mkdir(mode=0o700)
        Path("/var/lib/leapview/home").mkdir(mode=0o700)
        for directory in ("/var/lib/leapview", "/var/lib/leapview/home"):
            os.chown(directory, 999, 999)
        Path("/var/lib/leapview-trust").mkdir(mode=0o755)
        Path("/root/support").mkdir(mode=0o700)
        self.postgres = prepare_postgres(support_docker=self.support_docker, run=run, root=Path("/root/support"),
                                        source=SOURCE, image=self.staged["images"]["postgres"]["image"], home_base=Path("/var/lib/leapview"))
        self.evidence["postgres"] = self.postgres["evidence"]
        self.docker("volume", "create", "--driver", "local", "--opt", "type=none", "--opt", "o=bind",
                    "--opt", "device=/var/lib/leapview", "--label", "com.docker.compose.project=leapview",
                    "--label", "com.docker.compose.volume=leapview-state", "leapview_leapview-state")

    def start_daemons(self):
        # bridge=none deletes docker0 even with private daemon storage. Start
        # support first, before the app daemon owns this namespace's bridge.
        for name, key, socket in (("support", "supportDataRoot", "/run/support-docker.sock"), ("app", "appDataRoot", "/var/run/docker.sock")):
            # Reopen at the original absolute path: containerd/build metadata
            # can contain data-root paths even when only images were staged.
            target = Path(self.staged[key])
            config = {"dns": ["172.30.0.1"]} if name == "app" else {}
            private_file("/root/" + name + "-daemon.json", config)
            argv = [self.dockerd_bin, "--config-file=/root/" + name + "-daemon.json", "--host=unix://" + socket,
                    "--data-root=" + str(target), "--exec-root=/run/" + name, "--pidfile=/run/" + name + ".pid",
                    "--userland-proxy=false", "--exec-opt=native.cgroupdriver=cgroupfs"]
            if name == "support":
                argv += ["--bridge=none", "--iptables=false", "--ip6tables=false", "--ip-forward=false", "--ip-masq=false"]
            self.launch(argv, name + "-docker")
            docker = self.docker if name == "app" else self.support_docker
            wait(lambda: docker("info", check=False).returncode == 0)

    def bootstrap(self):
        image = self.manifest["images"]["bootstrap"]["image"]
        payload = Path("/root/payload")
        payload.mkdir(mode=0o700)
        container = self.docker("create", "--pull=never", image).stdout.strip()
        try:
            self.docker("cp", container + ":/usr/local/share/leapview/deployment/.", payload)
        finally:
            self.docker("rm", container)
        config = {"schemaVersion": 1, "targetId": "managed-application-qualification", "adminEmail": "operator@example.invalid",
                  "domain": "localhost", "environment": "prod", "https": True, "image": image}
        private_file("/root/host-config.json", config)
        probe = Path("/root/pool-probe")
        probe.mkdir(mode=0o700)
        shutil.copyfile(payload / "compose.yaml", probe / "compose.yaml")
        private_file(probe / "deployment.env", "COMPOSE_PROJECT_NAME=leapview\nLEAPVIEW_IMAGE=" + image + "\nCOMPOSE_APP_BIND=127.0.0.1:8080\n")
        private_file(probe / "leapview.env", guest._pool_probe_environment((payload / "leapview.env.example").read_bytes(), self.postgres["urls"], config).decode())
        result = run("sh", "-ec", guest._pool_qualification_command(str(probe), "DOCKER_HOST=unix:///var/run/docker.sock"), timeout=900)
        private_file("/root/operator.json", guest._qualification_operator_config(result.stdout.encode(), self.postgres["urls"]).decode())
        run(self.controller, "host", "install", "--config", "/root/host-config.json", "--payload", payload,
            "--source-image", image, "--operator-config", "/root/operator.json", timeout=900)
        run(self.controller, "qualify", "first-publication", "--evidence-dir", "/root/first-publication",
            "--assets-root", SOURCE / "deploy/compose/qualification", "--preloaded-client-image", self.staged["helpers"]["clientImage"],
            "--preloaded-browser-image", self.staged["helpers"]["browserImage"],
            "--lifecycle-credential-file", "/root/lifecycle.json", timeout=1800)
        publication = json.loads(Path("/root/first-publication/first-publication-report.json").read_text())
        if publication.get("result") != "passed" or publication.get("readinessBefore") != 503 or publication.get("readinessAfter") != 200:
            raise RuntimeError("protected first publication did not complete")
        self.evidence["firstPublication"] = publication
        # Copy only the validated public report, never the credential export.
        private_file(self.args.evidence_dir / "first-publication.json", publication)
        app = self.docker("ps", "-q", "--filter", "label=com.docker.compose.service=leapview").stdout.split()
        if len(app) != 1:
            raise RuntimeError("first publication must leave exactly one bootstrap application")
        self.docker("stop", "--time", "120", app[0], timeout=150)
        stopped = json.loads(self.docker("inspect", app[0]).stdout)[0]["State"]
        if stopped["Running"] or stopped["OOMKilled"] or stopped["ExitCode"] != 0:
            raise RuntimeError("bootstrap application did not shut down cleanly")
        self.evidence["bootstrapStoppedCleanly"] = True
        for container in self.docker("ps", "-aq").stdout.split():
            self.docker("stop", "--time", "30", container, timeout=45)
            self.docker("rm", container)
        if self.docker("ps", "-aq").stdout.strip():
            raise RuntimeError("bootstrap helpers survived removal")

    def managed_profile(self):
        self.root.mkdir(mode=0o700)
        self.state.mkdir(mode=0o700)
        private_file(self.state / "ingress.json", {"publish": False})
        kamal = SOURCE / "deploy/managed/kamal"
        for name in ("Gemfile", "Gemfile.lock", "probe_host.rb", "maintenance_adapter.rb", "maintenance_config.rb"):
            shutil.copyfile(kamal / name, self.root / name)
        shutil.copytree(self.args.bundle_root, "/root/bundle")
        Path("/root/bin").mkdir(mode=0o700)
        wrapper = "#!/bin/sh\nexport BUNDLE_IGNORE_CONFIG=1 BUNDLE_FROZEN=1 BUNDLE_PATH=/root/bundle\nexec " + str(self.args.tools.resolve() / "bin/bundle") + ' "$@"\n'
        private_file("/root/bin/bundle", wrapper, 0o700)
        os.environ["PATH"] = "/root/bin:" + os.environ["PATH"]
        self.evidence["bundleWrapperSHA256"] = hashlib.sha256(wrapper.encode()).hexdigest()
        Path("/root/.ssh").mkdir(mode=0o700)
        for name in ("host", "operator"):
            run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", "/root/.ssh/" + name)
        shutil.copyfile("/root/.ssh/operator.pub", "/root/.ssh/authorized_keys")
        Path("/root/.ssh/authorized_keys").chmod(0o600)
        private_file("/root/.ssh/known_hosts", "127.0.0.1 " + Path("/root/.ssh/host.pub").read_text())
        private_file(self.root / "ssh_config", "Host 127.0.0.1\n  StrictHostKeyChecking yes\n  UserKnownHostsFile /root/.ssh/known_hosts\n  IdentityFile /root/.ssh/operator\n  ForwardAgent no\n")
        private_file("/root/sshd_config", "ListenAddress 127.0.0.1\nPort 22\nHostKey /root/.ssh/host\nAuthorizedKeysFile /root/.ssh/authorized_keys\nPermitRootLogin yes\nPasswordAuthentication no\nUsePAM no\nStrictModes yes\nSetEnv PATH=" + os.environ["PATH"] + "\n")
        run("sshd", "-t", "-f", "/root/sshd_config")
        self.launch([shutil.which("sshd"), "-D", "-e", "-f", "/root/sshd_config"], "ssh")
        wait(lambda: run("ssh", "-F", self.root / "ssh_config", "root@127.0.0.1", "true", check=False).returncode == 0)
        run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=localhost",
            "-addext", "subjectAltName=DNS:localhost", "-keyout", "/root/tls.key", "-out", "/root/tls.crt")
        # Parse the installed canonical environment with its existing reader.
        # The file's values use Compose dotenv escaping; ask Compose to resolve
        # them rather than copying raw dollars/backslashes into Kamal secrets.
        compose = json.loads(self.docker("compose", "--project-directory", "/opt/leapview", "--env-file", "/opt/leapview/deployment.env",
                            "-f", "/opt/leapview/compose.yaml", "config", "--format", "json").stdout)
        installed = compose["services"]["leapview"]["environment"]
        secrets = {key: installed[key] for key in ("LEAPVIEW_AGENT_CREDENTIAL_KEY", "LEAPVIEW_CSRF_KEY", "LEAPVIEW_METRICS_BEARER_TOKEN",
                   "LEAPVIEW_POSTGRES_CONTROL_URL", "LEAPVIEW_POSTGRES_DUCKLAKE_URL", "LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL", "LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL")}
        secrets.update(KAMAL_TLS_CERTIFICATE=Path("/root/tls.crt").read_text(), KAMAL_TLS_PRIVATE_KEY=Path("/root/tls.key").read_text(), KAMAL_REGISTRY_PASSWORD="offline-no-registry-access")
        if any("'" in value for value in secrets.values()):
            raise RuntimeError("qualification secrets require literal safe dotenv values")
        (self.root / ".kamal").mkdir(mode=0o700)
        private_file(self.root / ".kamal/secrets", "\n".join(key + "='" + value + "'" for key, value in secrets.items()) + "\n")
        private_file(self.root / "deploy.yml", (kamal / "maintenance_deploy.yml.example").read_text().replace(
            "  ssl: true", "  ssl:\n    certificate_pem: KAMAL_TLS_CERTIFICATE\n    private_key_pem: KAMAL_TLS_PRIVATE_KEY"))
        environment = {key: installed[key] for key in ("LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID", "LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST")}
        environment.update(KAMAL_APP_HOSTNAME="localhost", KAMAL_REGISTRY_USERNAME="qualification")
        private_file(self.root / "environment.json", environment)
        profile = {"version": 1, "target": "managed-application-qualification", "root": str(self.root), "stateRoot": str(self.state),
                   "home": "/var/lib/leapview/home", "socket": "/var/lib/leapview/home/maintenance.sock", "service": "leapview", "hostname": "localhost",
                   "proxyImage": self.staged["images"]["proxy"]["image"], "admissionRoot": str(self.args.work_root / "inputs/admissions")}
        profile["capacity"] = {"dockerRootDir": self.staged["appDataRoot"],
                               "home": {"freeBytes": 256 * 1024**2, "freeInodes": 1000},
                               "stateRoot": {"freeBytes": 16 * 1024**2, "freeInodes": 100},
                               "docker": {"freeBytes": 512 * 1024**2, "freeInodes": 1000}}
        private_file(self.profile_path, profile)
        os.environ["SSL_CERT_FILE"] = "/root/tls.crt"

    def control(self, action, request, **kwargs):
        return run(self.controller, "host", "managed-release", action, "--profile", self.profile_path, "--request", request, timeout=1200, **kwargs)

    def request(self, name, first, second, *, enrollment=False):
        selections = self.manifest["images"]
        projections = []
        for key in (first, second):
            selected = selections[key]
            projections.append(json.loads(run(self.controller, "host", "managed-release", "inspect", "--profile", self.profile_path,
                "--image", selected["image"], "--revision", selected["sourceRevision"]).stdout))
        value = release_request(selections[first], selections[second], *projections,
                                self.manifest["compatibility"]["sourceBefore"], enrollment=enrollment)
        return private_file("/root/" + name + ".json", value)

    def status(self, request, phase):
        state = json.loads(self.control("status", request).stdout)
        if state["phase"] != phase or not state["commitEstablished"]:
            raise RuntimeError("managed operation did not establish expected commit")
        return state

    def public_ready(self):
        return run("ip", "netns", "exec", "public", "curl", "--noproxy", "*", "--silent", "--show-error", "--fail",
                   "--connect-timeout", "2", "--max-time", "5", "--cacert", "/root/tls.crt",
                   "--resolve", "localhost:443:198.18.0.1", "https://localhost/readyz", check=False).returncode == 0

    def interrupted_recovery(self, request, workload):
        # The client trusts the private fixture CA; this one controller process
        # deliberately does not. It cannot persist a commit after publishing the
        # candidate, leaving an observable real interruption/recovery boundary.
        environment = dict(os.environ, SSL_CERT_FILE="/dev/null", SSL_CERT_DIR="/root/empty-ca")
        Path("/root/empty-ca").mkdir(mode=0o700)
        process = self.launch([self.controller, "host", "managed-release", "run", "--profile", self.profile_path,
                               "--request", request], "interrupted-handoff", env=environment)
        journal = self.state / "managed-image-operation.json"
        def provisional():
            if process.poll() is not None:
                raise RuntimeError("interrupted handoff exited before provisional publication")
            state = json.loads(journal.read_text())
            return state["phase"] == "opening-ingress" and not state["commitEstablished"] and self.public_ready()
        wait(provisional, timeout=600)
        acknowledged = workload.acknowledge_write(b"order_id,value\nmanaged-interruption,42\n")
        before = json.loads(journal.read_text())
        if before["phase"] != "opening-ingress" or before["commitEstablished"]:
            raise RuntimeError("candidate write did not occur before interrupted commit")
        process.send_signal(signal.SIGTERM)
        process.wait(timeout=210)
        if process.returncode == 0:
            raise RuntimeError("interrupted controller unexpectedly succeeded")
        stopped = json.loads(journal.read_text())
        if stopped["commitEstablished"] or stopped["phase"] != "opening-ingress":
            raise RuntimeError("interrupted journal crossed the commit boundary")
        if self.public_ready():
            raise RuntimeError("failed controller left public ingress open")
        self.control("recover", request)
        state = self.status(request, "recovered")
        workload.verify_acknowledgement(acknowledged)
        wait(self.public_ready)
        return {"beforeInterruption": before, "afterInterruption": stopped, "recovery": state,
                "candidateAcknowledgedWrite": acknowledged, "acknowledgementRetained": True, "ingressClosedOnFailure": True}

    def cleanup(self):
        errors = []
        for docker in (self.docker, self.support_docker):
            try:
                result = docker("ps", "-aq", check=False, timeout=10)
                if result.returncode == 0 and result.stdout.strip():
                    docker("rm", "--force", *result.stdout.split(), timeout=60)
            except Exception:
                errors.append("container cleanup")
        for process, output in reversed(self.processes):
            try:
                if process.poll() is None:
                    process.terminate()
                    process.wait(timeout=30)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=10)
                errors.append("forced process termination")
            finally:
                output.close()
        if errors:
            raise RuntimeError("qualification cleanup failed")


def offline(args):
    manifest = json.loads((args.work_root / "inputs/inputs.json").read_text())
    staged = json.loads((args.work_root / "staged.json").read_text())
    evidence = {"schemaVersion": 1, "scope": "isolated-managed-application-lifecycle", "fullManagedProfileQualified": False,
                "passed": False, "cleanupCompleted": False, "inputs": manifest, "stagedImages": staged["images"],
                "helpers": staged["helpers"], "helperBuildFiles": staged["helperBuildFiles"],
                "onlineNamespaceIsolated": staged["onlineNamespaceIsolated"],
                "userspaceNetworkStopped": staged["userspaceNetworkStopped"],
                "tools": str(args.tools.resolve()), "dockerPackage": str(args.docker_package.resolve()),
                "controllerSHA256": hashlib.sha256(args.controller.read_bytes()).hexdigest(), "stage": "namespace-preparation"}
    fixture_files = list(Path(__file__).parent.glob("managed_application*.py")) + [
        SOURCE / "scripts/managed_application_inputs.py", SOURCE / "scripts/managed_admission_handoff.py",
        SOURCE / "scripts/nix_compose_host_guest.py", SOURCE / "scripts/demo_upgrade_plan.py",
        SOURCE / "deploy/managed/kamal/Gemfile.lock"]
    evidence["fixtureSHA256"] = {str(path.relative_to(SOURCE)): hashlib.sha256(path.read_bytes()).hexdigest() for path in fixture_files}
    runtime = Runtime(args, manifest, staged, evidence)
    try:
        runtime.prepare()
        stage(evidence, "protected-first-publication")
        runtime.bootstrap()
        stage(evidence, "managed-profile")
        runtime.managed_profile()
        stage(evidence, "enrollment")
        enrollment = runtime.request("enrollment", "predecessor", "predecessor", enrollment=True)
        runtime.control("enroll", enrollment)
        evidence["enrollment"] = runtime.status(enrollment, "succeeded")
        wait(runtime.public_ready)
        from managed_application_workload import Workload
        workload = Workload(Path("/root/lifecycle.json"), Path("/root/tls.crt"))
        baseline = workload.query()
        acknowledged = workload.acknowledge_write(b"order_id,value\nmanaged-handoff,41\n")
        stage(evidence, "handoff")
        forward = runtime.request("forward", "predecessor", "candidate")
        with workload.open_sse() as stream:
            stream.wait_first_frame()
            runtime.control("run", forward)
            stream.wait_drained()
        evidence["dashboardStreamDrained"] = True
        evidence["handoff"] = runtime.status(forward, "succeeded")
        workload.verify_acknowledgement(acknowledged)
        if workload.query() != baseline:
            raise RuntimeError("published semantic query changed during compatible handoff")
        runtime.control("recover", forward)
        evidence["committedRecovery"] = runtime.status(forward, "succeeded")
        stage(evidence, "reverse-handoff")
        reverse = runtime.request("reverse", "candidate", "predecessor")
        runtime.control("run", reverse)
        evidence["reverseHandoff"] = runtime.status(reverse, "succeeded")
        stage(evidence, "interrupted-recovery")
        evidence["interruptedRecovery"] = runtime.interrupted_recovery(forward, workload)
        workload.verify_acknowledgement(acknowledged)
        if workload.query() != baseline:
            raise RuntimeError("published semantic query changed after recovery")
        evidence["workload"] = {"baselineQuery": baseline, "acknowledgedWrite": acknowledged, "acknowledgementRetained": True,
                                "publishedQueryUnchanged": True, "stagedWriteActivated": False}
        stage(evidence, "complete")
        evidence["passed"] = True
    finally:
        try:
            runtime.cleanup()
            evidence["cleanupCompleted"] = True
        except Exception:
            evidence["passed"] = False
            raise
        finally:
            private_file(args.evidence_dir / "application.json", evidence)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("tools", "docker-package", "controller", "verifier", "bundle-root", "work-root", "evidence-dir"):
        parser.add_argument("--" + name, type=Path, required=True)
    for name in ("bootstrap", "predecessor", "candidate"):
        parser.add_argument("--" + name + "-run", type=int, required=True)
    parser.add_argument("--namespace-parent", type=json.loads, help=argparse.SUPPRESS)
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("isolated application qualification requires Linux root")
    validate_visible_paths([SOURCE, args.tools, args.docker_package, args.controller, args.verifier, args.bundle_root, args.work_root, args.evidence_dir])
    os.chdir(SOURCE)
    if args.namespace_parent:
        offline(args)
        return
    args.evidence_dir.mkdir(mode=0o700)
    created, completed = False, False
    evidence = {"schemaVersion": 1, "scope": "isolated-managed-application-lifecycle", "fullManagedProfileQualified": False,
                "passed": False, "cleanupCompleted": False, "inputStorageRemoved": False, "stage": "authenticated-inputs"}
    try:
        args.work_root.mkdir(mode=0o700)
        created = True
        manifest = inputs.prepare({key: getattr(args, key + "_run") for key in ("bootstrap", "predecessor", "candidate")},
                                  root=args.work_root / "inputs", verifier=args.verifier, source_root=SOURCE)
        stage(evidence, "online-image-staging")
        from managed_application_staging import stage_images
        staged = stage_images(root=args.work_root, source=SOURCE, docker_package=args.docker_package,
                              tools=args.tools, selections=manifest["images"])
        private_file(args.work_root / "staged.json", staged, 0o400)
        stage(evidence, "offline-application")
        # No credentials or Docker configuration survive into the offline child.
        clean = {"PATH": os.environ["PATH"], "HOME": "/root"}
        subprocess.run(["unshare", "--mount", "--net", "--pid", "--fork", "--mount-proc", "--kill-child=SIGKILL", sys.executable, str(Path(__file__).resolve()),
                        *sys.argv[1:], "--namespace-parent", json.dumps(namespace_ids())], check=True, env=clean)
        report = json.loads((args.evidence_dir / "application.json").read_text())
        if report.get("passed") is not True or report.get("cleanupCompleted") is not True:
            raise RuntimeError("offline qualification did not produce a complete successful report")
        completed = True
    finally:
        report = args.evidence_dir / "application.json"
        if report.exists():
            evidence = json.loads(report.read_text())
        if not completed:
            evidence["passed"] = False
        try:
            if created:
                shutil.rmtree(args.work_root)
            evidence["inputStorageRemoved"] = True
        except Exception:
            evidence["passed"] = False
            raise
        finally:
            temporary = private_file(args.evidence_dir / ".application.json.tmp", evidence)
            temporary.replace(report)


if __name__ == "__main__":
    main()
