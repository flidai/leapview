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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("predecessor-image", "candidate-image", "proxy-image", "tools", "bundle-root", "docker-package", "evidence-dir"):
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument("--namespace-parent", type=json.loads, help=argparse.SUPPRESS)
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("root required; fixture creates isolated namespaces")
    evidence_dir = args.evidence_dir.resolve()
    hidden_roots = (Path("/root"), Path("/run"), Path("/var/lib"))
    for visible_path in (Path(__file__).resolve(), evidence_dir, *(getattr(args, key + "_image").resolve() for key in ("predecessor", "candidate", "proxy"))):
        if any(visible_path.is_relative_to(path) for path in hidden_roots):
            raise SystemExit("source, archives and evidence must remain outside fixture-hidden roots")
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
    registry_bin = str(args.tools.resolve() / "bin/registry")
    archives = {key: getattr(args, key + "_image").resolve() for key in ("predecessor", "candidate", "proxy")}
    kamal_source = Path(__file__).resolve().parents[2] / "kamal"
    bundle_source = args.bundle_root.resolve()
    staged_bundle = None
    if any(bundle_source.is_relative_to(path) for path in ("/root", "/run", "/var/lib")):
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
    for path in ("/run", "/root", "/var/lib"):
        run("mount", "-t", "tmpfs", "tmpfs", path)
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
        evidence.update(passed=True, images=references, kamalVersion="2.12.0", proxyVersion="v0.9.2", assertions=["private-ingress", "verified-custom-tls", "proxy-route-reboot", "offline-candidate-and-predecessor-boot", "acknowledged-file-write-preserved", "stream-frame-and-close", "clean-stop"], archiveSHA256={k: hashlib.sha256(v.read_bytes()).hexdigest() for k, v in archives.items()})
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
