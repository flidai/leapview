"""Receipt-free real Kamal/proxy transport qualification in private namespaces.

The application is a synthetic protocol fixture, not LeapView. Passing this test
does not qualify application admission, PostgreSQL, provider recovery, or ACME.
No production artifact-admission records are created or consumed.
"""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import select
import shutil
import subprocess
import sys
import tempfile
import time

sys.dont_write_bytecode = True
from docker_daemon_test import namespace_ids, verify_namespaces

HIDDEN_ROOTS = (Path("/root"), Path("/run"), Path("/var"))


def validate_visible_paths(paths):
    for path in paths:
        if any(Path(path).resolve().is_relative_to(hidden) for hidden in HIDDEN_ROOTS):
            raise SystemExit("source, archives, tools and evidence must remain outside fixture-hidden roots")


def prepare_private_var(root):
    # Pinned Nix OpenSSH requires /var/empty even when the runner's distro uses
    # another privilege-separation directory. /var is already a private tmpfs.
    (root / "empty").mkdir(mode=0o755)
    (root / "lib").mkdir(mode=0o755)
    (root / "run").symlink_to("/run")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("predecessor-image", "candidate-image", "proxy-image", "tools", "bundle-root", "docker-package", "controller", "evidence-dir"):
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument("--namespace-parent", type=json.loads, help=argparse.SUPPRESS)
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("root required; fixture creates isolated namespaces")
    evidence_dir = args.evidence_dir.resolve()
    validate_visible_paths((Path(__file__).resolve(), evidence_dir, args.tools, args.docker_package, args.controller, *(getattr(args, key + "_image") for key in ("predecessor", "candidate", "proxy"))))
    if args.bundle_root.resolve().is_relative_to("/var"):
        raise SystemExit("bundle cache must remain outside fixture-hidden /var")
    evidence_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    evidence = {"scope": "synthetic-kamal-transport-only", "fullManagedProfileQualified": False, "passed": False, "cleanupCompleted": False}
    (evidence_dir / "transport.json").write_text(json.dumps(evidence) + "\n")
    if args.namespace_parent is None:
        subprocess.run(["unshare", "--mount", "--net", "--pid", "--fork", "--mount-proc", sys.executable, str(Path(__file__).resolve()), *sys.argv[1:], "--namespace-parent", json.dumps(namespace_ids())], check=True)
        return
    verify_namespaces(args.namespace_parent)
    # Resolve /run/current-system paths before hiding the host runtime directory.
    os.environ["PATH"] = str(args.tools.resolve() / "bin") + ":" + ":".join(str(Path(p).resolve()) for p in os.environ["PATH"].split(":"))
    docker_bin = str(args.docker_package.resolve() / "bin/docker")
    dockerd_bin = str(args.docker_package.resolve() / "bin/dockerd")
    controller_bin = str(args.controller.resolve())
    registry_bin = str(args.tools.resolve() / "bin/registry")
    archives = {key: getattr(args, key + "_image").resolve() for key in ("predecessor", "candidate", "proxy")}
    kamal_source = Path(__file__).resolve().parents[2] / "kamal"
    bundle_source = args.bundle_root.resolve()
    staged_bundle = None
    if any(bundle_source.is_relative_to(path) for path in ("/root", "/run")):
        staged_bundle = tempfile.TemporaryDirectory(prefix="managed-transport-bundle-", dir="/tmp")
        shutil.copytree(bundle_source, Path(staged_bundle.name) / "bundle")
        bundle_source = Path(staged_bundle.name) / "bundle"
    passwd = Path("/etc/passwd").read_text()
    nix_system = Path("/run/current-system").resolve()
    processes = []
    stream = None

    def run(*argv, timeout=120, check=True, **kwargs):
        result = subprocess.run(argv, capture_output=True, text=True, timeout=timeout, **kwargs)
        if check and result.returncode:
            raise RuntimeError(f"fixture command {argv[0]} failed: {result.stderr[-4000:]}\n{result.stdout[-4000:]}")
        return result

    def wait(action, timeout=60):
        deadline = time.monotonic() + timeout
        while not action():
            if time.monotonic() > deadline:
                raise RuntimeError("fixture readiness deadline exceeded")
            time.sleep(.2)

    def launch(argv, name, **kwargs):
        log = open(Path("/root") / (name + ".log"), "w")
        process = subprocess.Popen(argv, stdout=log, stderr=log, **kwargs)
        processes.append((process, log))
        return process

    if {x["ifname"] for x in json.loads(run("ip", "-j", "link", "show").stdout)} != {"lo"}:
        raise SystemExit("isolated network was not empty")
    run("mount", "--make-rprivate", "/")
    for path in ("/run", "/root", "/var"):
        run("mount", "-t", "tmpfs", "tmpfs", path)
    prepare_private_var(Path("/var"))
    os.chmod("/root", 0o700)
    if str(nix_system).startswith("/nix/store/"):
        Path("/run/current-system").symlink_to(nix_system)
    # The host's NixOS login shell can live beneath hidden /run. Use a private
    # passwd view with an unlocked fixture root account and a store-bound shell;
    # password authentication remains disabled on the loopback-only sshd.
    private_passwd = Path("/root/passwd")
    private_passwd.write_text("\n".join("root::0:0:transport fixture:/root:" + str(args.tools.resolve() / "bin/bash") if line.startswith("root:") else line for line in passwd.splitlines()) + "\n")
    run("mount", "--bind", str(private_passwd), "/etc/passwd")
    private_shadow = Path("/root/shadow")
    private_shadow.write_text("root::1:0:99999:7:::\n")
    private_shadow.chmod(0o600)
    run("mount", "--bind", str(private_shadow), "/etc/shadow")
    Path("/run/netns").mkdir()
    run("ip", "link", "set", "lo", "up")
    run("ip", "netns", "add", "public")
    run("ip", "link", "add", "eth-public", "type", "veth", "peer", "name", "client")
    run("ip", "link", "set", "client", "netns", "public")
    run("ip", "addr", "add", "198.18.0.1/24", "dev", "eth-public")
    run("ip", "link", "set", "eth-public", "up")
    run("ip", "netns", "exec", "public", "ip", "addr", "add", "198.18.0.2/24", "dev", "client")
    run("ip", "netns", "exec", "public", "ip", "link", "set", "client", "up")
    run("ip", "netns", "exec", "public", "ip", "link", "set", "lo", "up")
    run("bash", str(Path(__file__).resolve().parent.parent / "modules/docker-firewall.sh"), "eth-public")
    root = Path("/root/managed")
    root.mkdir(mode=0o700)
    for name in ("Gemfile", "Gemfile.lock", "probe_host.rb", "maintenance_adapter.rb", "maintenance_config.rb"):
        shutil.copyfile(kamal_source / name, root / name)
    shutil.copytree(bundle_source, "/root/bundle")
    Path("/root/.ssh").mkdir(mode=0o700)
    Path("/var/lib/leapview/home").mkdir(parents=True, mode=0o700)
    Path("/var/lib/leapview-trust").mkdir(mode=0o755)
    for name in ("host", "operator"):
        run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", "/root/.ssh/" + name)
    shutil.copyfile("/root/.ssh/operator.pub", "/root/.ssh/authorized_keys")
    os.chmod("/root/.ssh/authorized_keys", 0o600)
    Path("/root/.ssh/known_hosts").write_text("127.0.0.1 " + Path("/root/.ssh/host.pub").read_text())
    (root / "ssh_config").write_text("Host 127.0.0.1\n  StrictHostKeyChecking yes\n  UserKnownHostsFile /root/.ssh/known_hosts\n  IdentityFile /root/.ssh/operator\n  ForwardAgent no\n")
    remote_path = str(args.docker_package.resolve() / "bin") + ":" + os.environ["PATH"]
    Path("/root/sshd_config").write_text("ListenAddress 127.0.0.1\nPort 22\nHostKey /root/.ssh/host\nAuthorizedKeysFile /root/.ssh/authorized_keys\nPermitRootLogin yes\nPasswordAuthentication no\nUsePAM no\nStrictModes yes\nSetEnv PATH=" + remote_path + "\n")
    run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=managed.fixture.invalid", "-addext", "subjectAltName=DNS:managed.fixture.invalid", "-keyout", "/root/tls.key", "-out", "/root/tls.crt")
    secrets = {"KAMAL_TLS_CERTIFICATE": Path("/root/tls.crt").read_text(), "KAMAL_TLS_PRIVATE_KEY": Path("/root/tls.key").read_text()}
    for key in ("KAMAL_REGISTRY_PASSWORD", "LEAPVIEW_AGENT_CREDENTIAL_KEY", "LEAPVIEW_CSRF_KEY", "LEAPVIEW_METRICS_BEARER_TOKEN", "LEAPVIEW_POSTGRES_CONTROL_URL", "LEAPVIEW_POSTGRES_DUCKLAKE_URL", "LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL", "LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL"):
        secrets[key] = "transport-fixture-only"
    (root / ".kamal").mkdir(mode=0o700)
    # Dotenv 3 preserves escaped backslash-n by default. These fixture-controlled
    # PEM/base64 values contain no apostrophes: literal quoted multiline values
    # preserve PEM bytes without enabling legacy parser behavior.
    assert all("'" not in value for value in secrets.values())
    (root / ".kamal/secrets").write_text("\n".join(key + "='" + value + "'" for key, value in secrets.items()) + "\n")
    os.chmod(root / ".kamal/secrets", 0o600)
    template = (kamal_source / "maintenance_deploy.yml.example").read_text().replace("  ssl: true", "  ssl:\n    certificate_pem: KAMAL_TLS_CERTIFICATE\n    private_key_pem: KAMAL_TLS_PRIVATE_KEY")
    (root / "deploy.yml").write_text(template)
    Path("/root/registry.yml").write_text("version: 0.1\nstorage:\n  filesystem:\n    rootdirectory: /root/registry-data\nhttp:\n  addr: 127.0.0.1:5000\n")
    Path("/root/daemon.json").write_text("{}")
    env = dict(PATH=remote_path, HOME="/root", BUNDLE_PATH="/root/bundle", KAMAL_APP_HOSTNAME="managed.fixture.invalid", KAMAL_REGISTRY_USERNAME="fixture", LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID="transport-fixture", LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST="transport-fixture", TMPDIR="/root", DOCKER_TMPDIR="/root", XDG_RUNTIME_DIR="/run")
    os.environ.clear()
    os.environ.update(env)
    def docker(*argv, **kwargs):
        return run(docker_bin, "--host", "unix:///var/run/docker.sock", *argv, **kwargs)
    def public(path="/readyz", *extra, check=True):
        return run("ip", "netns", "exec", "public", "curl", "--noproxy", "*", "--silent", "--show-error", "--fail", "--connect-timeout", "2", "--max-time", "10", "--cacert", "/root/tls.crt", "--resolve", "managed.fixture.invalid:443:198.18.0.1", *extra, "https://managed.fixture.invalid" + path, check=check)
    try:
        evidence["controllerSHA256"] = hashlib.sha256(Path(controller_bin).read_bytes()).hexdigest()
        # Fail immediately with the daemon's diagnostic, rather than polling a
        # listener for a minute when the runner lacks an OpenSSH prerequisite.
        run(shutil.which("sshd"), "-t", "-f", "/root/sshd_config")
        launch([dockerd_bin, "--config-file=/root/daemon.json", "--host=unix:///var/run/docker.sock", "--data-root=/var/lib/docker", "--exec-root=/run/docker", "--pidfile=/run/docker.pid", "--firewall-backend=iptables", "--userland-proxy=false", "--exec-opt=native.cgroupdriver=cgroupfs"], "docker", env=env)
        wait(lambda: docker("info", check=False).returncode == 0)
        launch([shutil.which("sshd"), "-D", "-e", "-f", "/root/sshd_config"], "ssh", env=env)
        wait(lambda: run("ssh", "-F", str(root / "ssh_config"), "root@127.0.0.1", "true", check=False).returncode == 0)
        registry = launch([registry_bin, "serve", "/root/registry.yml"], "registry", env=env)
        wait(lambda: run("curl", "--noproxy", "*", "-sf", "http://127.0.0.1:5000/v2/", check=False).returncode == 0)
        references = {}
        for kind, archive in archives.items():
            output = docker("load", "--input", str(archive), timeout=300).stdout
            loaded = [line.removeprefix("Loaded image: ") for line in output.splitlines() if line.startswith("Loaded image: ")]
            if len(loaded) != 1:
                raise RuntimeError("fixture archive must contain one tagged image")
            if kind == "proxy" and loaded[0] != "basecamp/kamal-proxy:v0.9.2":
                raise RuntimeError("transport fixture requires the pinned proxy v0.9.2 archive")
            if kind != "proxy":
                expected_revision = ("a" if kind == "predecessor" else "b") * 40
                metadata = json.loads(docker("image", "inspect", loaded[0]).stdout)[0]
                assert metadata["Config"]["Labels"]["org.opencontainers.image.revision"] == expected_revision
            tag = "127.0.0.1:5000/managed-transport/" + kind + ":fixture"
            docker("tag", loaded[0], tag)
            docker("push", tag, timeout=300)
            digests = json.loads(docker("image", "inspect", tag).stdout)[0]["RepoDigests"]
            references[kind] = next(d for d in digests if d.startswith(tag.split(":fixture")[0] + "@sha256:"))
        registry.terminate()
        registry.wait(timeout=30)
        assert run("curl", "--noproxy", "*", "-sf", "http://127.0.0.1:5000/v2/", check=False).returncode != 0
        docker("network", "create", "kamal")
        gate = Path("/root/ingress.json")
        def set_gate(publish):
            gate.write_text(json.dumps({"publish": publish}))
            gate.chmod(0o600)
        lock = open("/root/controller.lock", "w")
        os.chmod(lock.name, 0o600)
        fcntl.flock(lock, fcntl.LOCK_EX)
        def kamal(kind, *argv):
            revision = ("a" if kind == "predecessor" else "b") * 40
            controlled = dict(env, LEAPVIEW_MANAGED_IMAGE=references[kind], LEAPVIEW_MANAGED_PROXY_IMAGE=references["proxy"], LEAPVIEW_MANAGED_GATE=str(gate), LEAPVIEW_MANAGED_LOCK_FD=str(lock.fileno()), LEAPVIEW_MANAGED_LOCK_PATH=lock.name, LEAPVIEW_MANAGED_DEADLINE_UNIX_MS=str(int((time.time() + 90) * 1000)))
            print("transport fixture:", kind, *argv, flush=True)
            return run("bundle", "exec", "ruby", "-r", "./maintenance_adapter.rb", "-S", "kamal", *argv, "--config-file", "deploy.yml", "--version", revision, "--skip-hooks", cwd=root, env=controlled, pass_fds=(lock.fileno(),), start_new_session=True, timeout=100)
        def control(kind, action):
            body = json.dumps({"revision": ("a" if kind == "predecessor" else "b") * 40, "operation": "sha256:" + "c" * 64})
            return run("curl", "--silent", "--show-error", "--fail", "--unix-socket", "/var/lib/leapview/home/maintenance.sock", "-X", "POST", "-d", body, "http://maintenance/" + action)
        def qualify_capacity():
            # Exercise the shipped controller against this real private daemon.
            # No release request or artifact-admission record is manufactured.
            journal = Path("/var/lib/leapviewctl")
            journal.mkdir(mode=0o700)
            docker_root = docker("info", "--format", "{{.DockerRootDir}}").stdout.strip()
            assert docker_root == "/var/lib/docker"
            paths = ["/var/lib/leapview/home", str(journal), docker_root]
            assert len({os.stat(path).st_dev for path in paths}) == 1
            reserve = {"freeBytes": 1048576, "freeInodes": 10}
            profile = {
                "version": 1, "target": "transport-fixture", "root": str(root),
                "stateRoot": str(journal), "home": paths[0],
                "socket": paths[0] + "/maintenance.sock", "service": "leapview",
                "hostname": "managed.fixture.invalid", "proxyImage": references["proxy"],
                "admissionRoot": "/var/lib/leapview-admission",
                "capacity": {"dockerRootDir": docker_root, "home": dict(reserve),
                             "stateRoot": dict(reserve), "docker": dict(reserve)},
            }
            profile_path = root / "capacity-profile.json"
            command = [controller_bin, "host", "managed-release", "capacity", "--profile", str(profile_path)]
            predecessor_name = "leapview-web-" + "a" * 40
            before = json.loads(docker("inspect", predecessor_name).stdout)[0]
            gate_before = gate.read_bytes()

            def diagnostic():
                profile_path.write_text(json.dumps(profile))
                profile_path.chmod(0o600)
                result = run(*command, env=env, check=False, timeout=30)
                report = json.loads(result.stdout)
                assert isinstance(report["passed"], bool)
                return result, report

            def unchanged_predecessor():
                assert list(journal.iterdir()) == [], "read-only capacity check mutated the journal"
                assert gate.read_bytes() == gate_before, "capacity check changed ingress"
                after = json.loads(docker("inspect", predecessor_name).stdout)[0]
                assert (after["Id"], after["State"]["StartedAt"]) == (before["Id"], before["State"]["StartedAt"])
                assert after["State"]["Running"] and public().stdout == "a" * 40

            healthy_result, healthy = diagnostic()
            assert healthy_result.returncode == 0 and healthy["passed"], healthy
            assert len(healthy["filesystems"]) == 1, healthy
            measured = healthy["filesystems"][0]
            assert {item["path"] for item in measured["paths"]} == set(paths), measured
            assert {item["role"] for item in measured["paths"]} == {"home", "stateRoot", "docker"}, measured
            assert measured["device"] == "linux-device:" + str(os.stat(paths[0]).st_dev), measured
            assert measured["requiredBytes"] == 3 * reserve["freeBytes"], measured
            assert measured["requiredInodes"] == 3 * reserve["freeInodes"], measured
            actual = os.statvfs(paths[0])
            assert 0 < measured["freeBytes"] <= actual.f_blocks * actual.f_frsize
            assert 0 < measured["freeInodes"] <= actual.f_files
            unchanged_predecessor()
            # Exceed the entire filesystem, so concurrent daemon bookkeeping
            # cannot turn this deterministic rejection into a successful check.
            profile["capacity"]["docker"]["freeBytes"] = actual.f_blocks * actual.f_frsize + 1
            rejected_result, rejected = diagnostic()
            assert rejected_result.returncode != 0 and not rejected["passed"], rejected
            assert "insufficient free capacity" in rejected.get("error", ""), rejected
            assert rejected["filesystems"][0]["requiredBytes"] > rejected["filesystems"][0]["freeBytes"]
            unchanged_predecessor()
            profile["capacity"]["docker"] = dict(reserve, freeInodes=actual.f_files + 1)
            inode_result, inode_rejected = diagnostic()
            assert inode_result.returncode != 0 and not inode_rejected["passed"], inode_rejected
            assert "insufficient free capacity" in inode_rejected.get("error", ""), inode_rejected
            assert inode_rejected["filesystems"][0]["requiredInodes"] > inode_rejected["filesystems"][0]["freeInodes"]
            unchanged_predecessor()
            profile["capacity"]["docker"] = dict(reserve)
            profile["capacity"]["dockerRootDir"] = "/var/lib/mismatched-docker"
            mismatch_result, mismatch = diagnostic()
            assert mismatch_result.returncode != 0 and not mismatch["passed"], mismatch
            assert "Docker data root differs" in mismatch.get("error", ""), mismatch
            unchanged_predecessor()
            evidence["capacity"] = {"healthy": healthy, "bytesRejected": rejected,
                                    "inodesRejected": inode_rejected, "dockerRootMismatch": mismatch,
                                    "predecessorUnchanged": True}
        set_gate(False)
        kamal("predecessor", "proxy", "reboot", "--confirmed")
        for index, kind in enumerate(("predecessor", "candidate", "predecessor")):
            kamal(kind, "app", "boot")
            assert public(check=False).returncode != 0, "private proxy exposed the application"
            control(kind, "prepare")
            control(kind, "open")
            set_gate(True)
            kamal(kind, "proxy", "reboot", "--confirmed")
            wait(lambda: public(check=False).returncode == 0)
            assert public().stdout == ("a" if kind == "predecessor" else "b") * 40
            if index == 0:
                qualify_capacity()
            stream = subprocess.Popen(["ip", "netns", "exec", "public", "curl", "--noproxy", "*", "--silent", "--show-error", "--fail", "--no-buffer", "--max-time", "90", "--cacert", "/root/tls.crt", "--resolve", "managed.fixture.invalid:443:198.18.0.1", "https://managed.fixture.invalid/updates"], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            assert select.select([stream.stdout], [], [], 10)[0], "proxy buffered the fixture SSE frame"
            assert stream.stdout.readline() == "event: transport-fixture\n"
            assert stream.stdout.readline() == "data: " + ("a" if kind == "predecessor" else "b") * 40 + "\n"
            if kind == "candidate":
                payload = "candidate-acknowledged-write\n" * 4096
                assert public("/write", "--data-binary", payload).stdout == "b" * 40
            elif index == 2:
                assert public("/read").stdout == "candidate-acknowledged-write\n" * 4096
            control(kind, "close")
            _, stream_error = stream.communicate(timeout=10)
            assert stream.returncode == 0, "fixture SSE did not close cleanly: " + stream_error
            assert public(check=False).returncode != 0, "closed protocol admission accepted ingress"
            set_gate(False)
            kamal(kind, "proxy", "reboot", "--confirmed")
            assert public(check=False).returncode != 0
            kamal(kind, "app", "stop")
            stopped = json.loads(docker("inspect", "leapview-web-" + ("a" if kind == "predecessor" else "b") * 40).stdout)[0]
            assert not stopped["State"]["Running"] and stopped["State"]["ExitCode"] == 0
        evidence.update(passed=True, images=references, kamalVersion="2.12.0", proxyVersion="v0.9.2", assertions=["private-ingress", "verified-custom-tls", "proxy-route-reboot", "offline-candidate-and-predecessor-boot", "acknowledged-file-write-preserved", "stream-frame-and-close", "clean-stop", "real-filesystem-capacity", "capacity-rejection-preserves-predecessor"], archiveSHA256={k: hashlib.sha256(v.read_bytes()).hexdigest() for k, v in archives.items()})
    finally:
        if stream and stream.poll() is None:
            stream.terminate()
            stream.wait(timeout=10)
        for process, log in reversed(processes):
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)
            log.close()
            contents = Path(log.name).read_bytes()[-16000:]
            (evidence_dir / Path(log.name).name).write_bytes(contents)
        if staged_bundle:
            staged_bundle.cleanup()
        evidence["cleanupCompleted"] = all(process.poll() is not None for process, _ in processes)
        (evidence_dir / "transport.json").write_text(json.dumps(evidence, sort_keys=True) + "\n")
        print(json.dumps(evidence, sort_keys=True))


if __name__ == "__main__":
    main()
