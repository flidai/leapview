"""Stage private Docker stores online for later offline namespace restart.

No host daemon, image export/import, or registry republishing is involved. The
caller retains both raw data roots until isolated application qualification ends.
Images in this module are inputs, not newly issued artifact admission decisions.
"""
from __future__ import annotations

from contextlib import ExitStack
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import select
import subprocess
import sys
import time

from managed_application_support import guest
from docker_daemon_test import namespace_ids


class StagingError(RuntimeError):
    """Bounded staging failure without subprocess output or credentials."""


IMAGE = re.compile(r"ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}\Z")
REVISION = re.compile(r"[0-9a-f]{40}\Z")
IDENTITY = re.compile(r"sha256:[0-9a-f]{64}\Z")
PLATFORM = "linux/amd64"


def _canonical(value, label):
    path = Path(value)
    if not path.is_absolute() or path != path.resolve() or any(c in str(path) for c in "\x00\r\n"):
        raise StagingError(label + " must be a canonical absolute path")
    return path


def _locked_images(source):
    nix = (source / "deploy/managed/nixos/flake.nix").read_text()
    proxy = re.search(r'kamal-transport-proxy\s*=\s*pkgs\.dockerTools\.pullImage\s*\{([^}]+)\}', nix)
    if not proxy:
        raise StagingError("source-locked Kamal proxy is unavailable")
    name = re.search(r'imageName\s*=\s*"(basecamp/kamal-proxy)"', proxy[1])
    digest = re.search(r'imageDigest\s*=\s*"(sha256:[0-9a-f]{64})"', proxy[1])
    version = re.search(r'finalImageTag\s*=\s*"(v0\.9\.2)"', proxy[1])
    if not name or not digest or not version:
        raise StagingError("source-locked Kamal proxy identity is invalid")
    environment = (source / "deploy/compose/deployment.env.example").read_text()
    caddy = re.findall(r'^CADDY_IMAGE=(caddy:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64})$', environment, re.MULTILINE)
    if len(caddy) != 1:
        raise StagingError("source-locked Caddy identity is invalid")
    return {"proxy": name[1]+"@"+digest[1], "caddy": caddy[0],
            "postgres": guest._locked_postgres_image(source)}


def _repository_digest(reference):
    repository, digest = reference.split("@")
    if repository.startswith("docker.io/library/postgres:"):
        repository = "postgres"
    else:
        repository = repository.split(":")[0]
    return repository + "@" + digest


def _verify_staging_namespace(parent_namespaces):
    current = namespace_ids()
    if (not isinstance(parent_namespaces, dict) or set(parent_namespaces) != set(current)
            or os.geteuid() != 0 or os.getpid() != 1
            or any(current[kind] == parent_namespaces[kind] for kind in current)):
        raise StagingError("staging requires isolated mount, network and PID namespaces as root PID 1")


def _stage_images_in_namespace(*, root: Path, source: Path, docker_package: Path,
                              selections: dict, parent_namespaces: dict,
                              run=subprocess.run, popen=subprocess.Popen) -> dict:
    """Stage exact inputs and helpers; run/popen use subprocess-compatible APIs.

    ``root`` is an existing owner-private directory beneath /tmp. Daemon startup
    requires Linux root. The function never reuses an existing staging directory.
    Returned helper IDs belong to the app store; their containers must be removed
    after first publication and before managed controller inventory checks.
    """
    # Docker's bridge=none still removes docker0. Network flags cannot replace
    # namespace isolation, including when this private function is called directly.
    _verify_staging_namespace(parent_namespaces)
    root = _canonical(root, "staging root")
    source = _canonical(source, "source root")
    docker_package = _canonical(docker_package, "Docker package")
    if Path("/tmp") not in root.parents:
        raise StagingError("staging requires a distinct private directory below /tmp")
    info = root.lstat()
    if not root.is_dir() or info.st_uid != os.geteuid() or info.st_mode & 0o077:
        raise StagingError("staging root must be owner-private")
    if set(selections) != {"bootstrap", "predecessor", "candidate"}:
        raise StagingError("all three authenticated application selections are required")
    for selected in selections.values():
        if (not isinstance(selected, dict) or not isinstance(selected.get("image"), str)
                or not IMAGE.fullmatch(selected["image"])
                or not isinstance(selected.get("sourceRevision"), str)
                or not REVISION.fullmatch(selected["sourceRevision"])):
            raise StagingError("application staging requires immutable image and source identities")
    docker, dockerd = (docker_package / "bin" / name for name in ("docker", "dockerd"))
    if any(not binary.is_file() or not os.access(binary, os.X_OK) for binary in (docker, dockerd)):
        raise StagingError("pinned Docker package executables are unavailable")
    locked = _locked_images(source)
    assets = source / "deploy/compose/qualification"
    build_files = {}
    for name in ("Dockerfile.authoring-client", "Dockerfile.authoring-browser", "package.json", "package-lock.json"):
        path = assets / name
        if path.is_symlink() or not path.is_file() or path.stat().st_size > 2 * 1024**2:
            raise StagingError("protected authoring build input is unavailable")
        build_files[name] = "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()
    stage = root / "staging"
    try:
        stage.mkdir(mode=0o700)
    except OSError:
        raise StagingError("online staging requires a fresh private directory") from None
    processes = []
    stores = {}
    try:
        with ExitStack() as stack:
            try:
                for name in ("app", "support"):
                    directory = stage / name
                    directory.mkdir(mode=0o700)
                    for leaf in ("data", "exec", "home", "config", "tmp", "runtime", "cache"):
                        (directory / leaf).mkdir(mode=0o700)
                    config = directory / "daemon.json"
                    config.write_text("{}\n")
                    config.chmod(0o600)
                    client_config = directory / "config/config.json"
                    client_config.write_text("{}\n")
                    client_config.chmod(0o600)
                    environment = {
                        "PATH": str(docker_package / "bin") + ":" + os.environ.get("PATH", ""),
                        "HOME": str(directory / "home"), "DOCKER_CONFIG": str(directory / "config"),
                        "XDG_RUNTIME_DIR": str(directory / "runtime"), "XDG_CACHE_HOME": str(directory / "cache"),
                        **{key: str(directory / "tmp") for key in ("TMPDIR", "TMP", "TEMP", "DOCKER_TMPDIR")},
                    }
                    for key in ("SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
                                "http_proxy", "https_proxy", "all_proxy", "no_proxy"):
                        if key in os.environ:
                            environment[key] = os.environ[key]
                    socket = "unix://" + str(directory / "docker.sock")
                    log_path = directory / "staging.log"
                    descriptor = os.open(log_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
                    log = stack.enter_context(os.fdopen(descriptor, "w"))
                    process = popen([
                        str(dockerd), "--config-file="+str(config), "--host="+socket,
                        "--data-root="+str(directory / "data"), "--exec-root="+str(directory / "exec"),
                        "--pidfile="+str(directory / "docker.pid"), "--bridge=none", "--iptables=false",
                        "--ip6tables=false", "--ip-forward=false", "--ip-masq=false", "--userland-proxy=false",
                        "--exec-opt=native.cgroupdriver=cgroupfs",
                    ], stdout=log, stderr=subprocess.STDOUT, env=environment, close_fds=True, start_new_session=True)
                    processes.append(process)
                    stores[name] = {"root": directory, "socket": socket, "environment": environment, "log": log}

                def invoke(store_name, *arguments, timeout=120, capture=False, check=True):
                    store = stores[store_name]
                    options = dict(env=store["environment"], timeout=timeout, text=True, check=False)
                    if capture:
                        options.update(stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                    else:
                        options.update(stdout=store["log"], stderr=subprocess.STDOUT)
                    result = run([str(docker), "--host", store["socket"], *arguments], **options)
                    if check and result.returncode:
                        raise StagingError("online Docker " + arguments[0] + " failed")
                    return result

                deadline = time.monotonic() + 90
                for (name, _), process in zip(stores.items(), processes):
                    while True:
                        if process.poll() is not None:
                            raise StagingError("private staging daemon exited during startup")
                        if invoke(name, "info", timeout=10, check=False).returncode == 0:
                            break
                        if time.monotonic() >= deadline:
                            raise StagingError("private staging daemon startup deadline exceeded")
                        time.sleep(0.2)

                def inspect(store_name, reference, *, revision=None, require_digest=True):
                    output = invoke(store_name, "image", "inspect", reference, capture=True, timeout=30).stdout
                    if len(output) > 1024**2:
                        raise StagingError("staged image identity inventory exceeds its bound")
                    metadata = json.loads(output)
                    if not isinstance(metadata, list) or len(metadata) != 1 or not isinstance(metadata[0], dict):
                        raise StagingError("staged image identity inventory is invalid")
                    metadata = metadata[0]
                    identity, digests = metadata.get("Id", ""), metadata.get("RepoDigests", [])
                    if (not isinstance(identity, str) or not IDENTITY.fullmatch(identity)
                            or metadata.get("Os") != "linux" or metadata.get("Architecture") != "amd64"
                            or not isinstance(digests, list) or any(not isinstance(value, str) for value in digests)
                            or (require_digest and _repository_digest(reference) not in digests)
                            or (revision is not None and metadata.get("Config", {}).get("Labels", {}).get("org.opencontainers.image.revision") != revision)):
                        raise StagingError("staged image identity differs from its immutable input")
                    evidence = {"image": reference, "imageID": identity, "repoDigests": digests, "platform": PLATFORM}
                    if require_digest:
                        evidence["repositoryDigest"] = _repository_digest(reference)
                    return evidence

                images = {}
                pulled = set()
                for role, reference in [(role, selections[role]["image"]) for role in ("bootstrap", "predecessor", "candidate")] + list(locked.items()):
                    store_name = "support" if role == "postgres" else "app"
                    if (store_name, reference) not in pulled:
                        invoke(store_name, "pull", "--platform="+PLATFORM, reference, timeout=1200)
                        pulled.add((store_name, reference))
                    images[role] = inspect(store_name, reference, revision=selections[role]["sourceRevision"] if role in selections else None)
                for role, alias in (("proxy", "basecamp/kamal-proxy:v0.9.2"), ("caddy", locked["caddy"].split("@")[0])):
                    invoke("app", "tag", locked[role], alias)
                    if inspect("app", alias, require_digest=False)["imageID"] != images[role]["imageID"]:
                        raise StagingError("retained service alias differs from immutable image identity")
                    images[role]["alias"] = alias

                helpers = {}
                for key, recipe in (("clientImage", "Dockerfile.authoring-client"), ("browserImage", "Dockerfile.authoring-browser")):
                    tag = "leapview-managed-" + key.lower() + ":qualification"
                    arguments = ["build", "--network=host", "--platform="+PLATFORM, "--file", str(assets / recipe), "--tag", tag]
                    if key == "clientImage":
                        arguments += ["--build-arg", "LEAPVIEW_IMAGE="+selections["bootstrap"]["image"]]
                    invoke("app", *arguments, str(assets), timeout=1800)
                    helpers[key] = inspect("app", tag, require_digest=False)["imageID"]
                result = {"appDataRoot": str(stores["app"]["root"] / "data"),
                          "supportDataRoot": str(stores["support"]["root"] / "data"),
                          "images": images, "helpers": helpers, "helperBuildFiles": build_files,
                          "platform": PLATFORM, "daemonsStoppedGracefully": True}
            finally:
                shutdown_failed = False
                for process in reversed(processes):
                    try:
                        try:
                            process.terminate()
                        except ProcessLookupError:
                            pass
                        if process.wait(timeout=60) != 0:
                            shutdown_failed = True
                    except subprocess.TimeoutExpired:
                        shutdown_failed = True
                        try:
                            process.kill()
                            process.wait(timeout=30)
                        except Exception:
                            pass
                    except Exception:
                        shutdown_failed = True
                if shutdown_failed:
                    raise StagingError("private staging daemon graceful shutdown failed")
        return result
    except StagingError:
        raise
    except Exception:
        raise StagingError("online image staging failed") from None


def _await_ready(descriptor, process, timeout=60):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise StagingError("isolated staging helper exited before readiness")
        if select.select([descriptor], [], [], min(0.2, max(0, deadline-time.monotonic())))[0]:
            if os.read(descriptor, 1) == b"1":
                return
            raise StagingError("isolated staging readiness pipe closed")
    raise StagingError("isolated staging readiness deadline exceeded")


def _worker_pid(process, parent_namespaces):
    children = Path(f"/proc/{process.pid}/task/{process.pid}/children").read_text().split()
    if len(children) != 1 or not children[0].isdigit():
        raise StagingError("isolated staging requires exactly one namespace init")
    pid = int(children[0])
    status = Path(f"/proc/{pid}/status").read_text()
    inner = re.search(r"^NSpid:\s+([\d\s]+)$", status, re.MULTILINE)
    if (not inner or inner[1].split()[-1] != "1"
            or any(os.readlink(f"/proc/{pid}/ns/{kind}") == identity
                   for kind, identity in parent_namespaces.items())):
        raise StagingError("online staging child did not establish private namespaces")
    return pid


def _write_private(path, value):
    with Path(path).open("x") as stream:
        os.fchmod(stream.fileno(), 0o600)
        stream.write(value if isinstance(value, str) else json.dumps(value) + "\n")


def _require_parent_root(root):
    if (Path("/tmp") not in root.parents or not root.is_dir()
            or root.stat().st_uid != os.geteuid() or root.stat().st_mode & 0o077
            or os.geteuid() != 0):
        raise StagingError("online staging requires a root-owned private directory below /tmp")


def stage_images(*, root: Path, source: Path, docker_package: Path, tools: Path,
                 selections: dict, popen=subprocess.Popen) -> dict:
    """Stage only in fresh namespaces, with userspace networking on the host.

    The only host-side network process is slirp4netns: it opens ordinary outbound
    sockets and creates the TAP and routes inside the already verified child.
    Host Docker, bridge interfaces, routes and netfilter are never configured.
    """
    root, source, docker_package, tools = (
        _canonical(value, label) for value, label in
        ((root, "staging root"), (source, "source root"),
         (docker_package, "Docker package"), (tools, "staging tools")))
    _require_parent_root(root)
    hidden = tuple(Path(value) for value in ("/root", "/run", "/var", "/etc"))
    if any(any(path.is_relative_to(prefix) for prefix in hidden)
           for path in (root, source, docker_package, tools, Path(sys.executable).resolve())):
        raise StagingError("online staging inputs must remain outside hidden runtime roots")
    for name in ("unshare", "slirp4netns", "mount", "ip"):
        if not os.access(tools / "bin" / name, os.X_OK):
            raise StagingError("pinned online staging namespace tools are unavailable")
    control = root / "staging-control"
    control.mkdir(mode=0o700)
    trust = next((Path(value).resolve() for value in (
        os.environ.get("SSL_CERT_FILE", ""), "/etc/ssl/certs/ca-certificates.crt", "/etc/ssl/certs/ca-bundle.crt")
        if value and Path(value).is_file()), None)
    if trust is None or trust.stat().st_size > 8 * 1024**2:
        raise StagingError("online staging requires a bounded public CA bundle")
    _write_private(control / "ca.pem", trust.read_text())
    parent_namespaces = namespace_ids()
    config = {"root": str(root), "source": str(source), "docker_package": str(docker_package),
              "tools": str(tools), "selections": selections, "parent_namespaces": parent_namespaces}
    _write_private(control / "request.json", config)
    environment = {"PATH": str(tools / "bin") + ":" + str(docker_package / "bin"),
                   "HOME": str(control), "TMPDIR": str(control), "SSL_CERT_FILE": str(control / "ca.pem")}
    worker = helper = None
    descriptors = set()
    def pipe():
        pair = os.pipe()
        descriptors.update(pair)
        return pair
    def close(descriptor):
        if descriptor in descriptors:
            os.close(descriptor)
            descriptors.remove(descriptor)
    with ExitStack() as stack:
        worker_log = stack.enter_context(open(control / "worker.log", "x"))
        helper_log = stack.enter_context(open(control / "network.log", "x"))
        os.fchmod(worker_log.fileno(), 0o600)
        os.fchmod(helper_log.fileno(), 0o600)
        try:
            worker_ready_read, worker_ready_write = pipe()
            release_read, release_write = pipe()
            worker = popen([str(tools / "bin/unshare"), "--mount", "--net", "--pid", "--fork",
                           "--mount-proc", "--kill-child=SIGKILL", str(Path(sys.executable).resolve()),
                           str(Path(__file__).resolve()), "--worker", str(control / "request.json"),
                           "--ready-fd", str(worker_ready_write), "--release-fd", str(release_read)],
                          env=environment, stdout=worker_log, stderr=subprocess.STDOUT,
                          pass_fds=(worker_ready_write, release_read), start_new_session=True)
            close(worker_ready_write)
            close(release_read)
            _await_ready(worker_ready_read, worker)
            pid = _worker_pid(worker, parent_namespaces)
            helper_ready_read, helper_ready_write = pipe()
            exit_read, exit_write = pipe()
            helper = popen([str(tools / "bin/slirp4netns"), "--configure", "--disable-host-loopback",
                           "--ready-fd="+str(helper_ready_write), "--exit-fd="+str(exit_read), str(pid), "tap0"],
                          env=environment, stdout=helper_log, stderr=subprocess.STDOUT,
                          pass_fds=(helper_ready_write, exit_read), start_new_session=True)
            close(helper_ready_write)
            close(exit_read)
            _await_ready(helper_ready_read, helper)
            os.write(release_write, b"1")
            close(release_write)
            if worker.wait(timeout=7200) != 0:
                raise StagingError("isolated online staging failed")
            close(exit_write)
            if helper.wait(timeout=30) != 0:
                raise StagingError("userspace staging network did not shut down cleanly")
            result = json.loads((control / "result.json").read_text())
            result["onlineNamespaceIsolated"] = True
            result["userspaceNetworkStopped"] = True
            return result
        except StagingError:
            raise
        except Exception:
            raise StagingError("isolated online staging failed") from None
        finally:
            cleanup_failed = False
            for process in (worker, helper):
                try:
                    if process is not None and process.poll() is None:
                        process.terminate()
                        try:
                            process.wait(timeout=30)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait(timeout=10)
                except Exception:
                    cleanup_failed = True
            for descriptor in list(descriptors):
                close(descriptor)
            if cleanup_failed:
                raise StagingError("isolated staging process cleanup failed")


def _worker(config_path, ready_fd, release_fd):
    config = json.loads(Path(config_path).read_text())
    _verify_staging_namespace(config["parent_namespaces"])
    tools = Path(config.pop("tools"))
    root = Path(config["root"])
    def command(*args):
        result = subprocess.run([str(arg) for arg in args], check=False, capture_output=True, text=True, timeout=30)
        if result.returncode:
            raise StagingError("isolated staging namespace preparation failed")
        return result
    links = json.loads(command(tools / "bin/ip", "-j", "link", "show").stdout)
    if {link["ifname"] for link in links} != {"lo"}:
        raise StagingError("online staging must begin with an empty isolated network")
    command(tools / "bin/mount", "--make-rprivate", "/")
    for path in ("/root", "/run", "/var", "/etc"):
        command(tools / "bin/mount", "-t", "tmpfs", "tmpfs", path)
        Path(path).chmod(0o700 if path == "/root" else 0o755)
    Path("/var/run").symlink_to("/run")
    _write_private("/etc/passwd", "root:x:0:0:staging:/root:" + str(tools / "bin/bash") + "\n")
    _write_private("/etc/group", "root:x:0:\n")
    _write_private("/etc/nsswitch.conf", "passwd: files\ngroup: files\nhosts: files dns\n")
    _write_private("/etc/hosts", "127.0.0.1 localhost\n::1 localhost\n")
    _write_private("/etc/resolv.conf", "nameserver 10.0.2.3\noptions timeout:2 attempts:3\n")
    for path in Path("/etc").iterdir():
        path.chmod(0o644)
    os.write(ready_fd, b"1")
    os.close(ready_fd)
    if not select.select([release_fd], [], [], 120)[0] or os.read(release_fd, 1) != b"1":
        raise StagingError("online staging network was not authorized by the parent")
    os.close(release_fd)
    links = json.loads(command(tools / "bin/ip", "-j", "link", "show").stdout)
    routes = json.loads(command(tools / "bin/ip", "-j", "route", "show", "default").stdout)
    if ({link["ifname"] for link in links} != {"lo", "tap0"}
            or len(routes) != 1 or routes[0].get("dev") != "tap0" or routes[0].get("gateway") != "10.0.2.2"):
        raise StagingError("online staging requires only the userspace network route")
    result = _stage_images_in_namespace(**config)
    _write_private(root / "staging-control/result.json", result)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--worker", type=Path, required=True)
    parser.add_argument("--ready-fd", type=int, required=True)
    parser.add_argument("--release-fd", type=int, required=True)
    args = parser.parse_args()
    _worker(args.worker, args.ready_fd, args.release_fd)
