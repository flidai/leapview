#!/usr/bin/env python3
"""Qualify site image retention with an isolated Docker 29 daemon and tiny images.

Requires the Distribution Registry 2.8.x binary (`docker-registry` package); set
REGISTRY_BINARY when it is extracted outside PATH. The fixture uses only loopback
registry traffic and locally imported images, with private daemon state under /tmp.
"""

from __future__ import annotations

import importlib.util
import io
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
from pathlib import Path
from typing import Any
from urllib.error import URLError
from urllib.request import ProxyHandler, Request, build_opener

# Loading the production helper from this isolated checkout must not leave cache
# artifacts beside its source.
sys.dont_write_bytecode = True


ROOT = Path(__file__).resolve().parents[1]
HELPER = ROOT / "deploy/hetzner-site/files/site_image_retention.py"
SITE_REPOSITORY = "ghcr.io/flidai/leapview-site"
REGISTRY_REPOSITORY = "127.0.0.1:{port}/site"
FOREIGN_REPOSITORY = "127.0.0.1:{port}/foreign"
REGISTRY_REQUEST = re.compile(r'"(?:GET|HEAD|POST|PATCH|PUT|DELETE) /v2/([^/\s?]+)(?:[/?\s])')


def load_helper():
    spec = importlib.util.spec_from_file_location("site_image_retention", HELPER)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load helper at {HELPER}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


RETENTION = load_helper()


def check(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def run(
    command: list[str],
    env: dict[str, str],
    *,
    input_bytes: bytes | None = None,
    timeout_s: float = 60,
) -> str:
    try:
        result = subprocess.run(
            command,
            input=input_bytes,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=env,
            check=False,
            timeout=timeout_s,
        )
    except subprocess.TimeoutExpired as exc:
        raise RuntimeError(f"command timed out after {timeout_s}s: {' '.join(command)}") from exc
    if result.returncode:
        raise RuntimeError(
            f"command failed ({result.returncode}): {' '.join(command)}\n"
            f"stdout: {result.stdout.decode(errors='replace')}\n"
            f"stderr: {result.stderr.decode(errors='replace')}"
        )
    return result.stdout.decode(errors="replace")


def stop_private_process(process: subprocess.Popen[bytes] | None) -> None:
    """Stop one process group created by this script, including partial-start children."""
    if process is None:
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=5)
    # A daemon can exit while leaving a child in its dedicated session. Reap
    # any such child without ever signaling a process from the shared daemon.
    try:
        os.killpg(process.pid, 0)
    except ProcessLookupError:
        return
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass


def qualify_pure_policy() -> None:
    digest = "sha256:" + "a" * 64
    other_digest = "sha256:" + "b" * 64
    obsolete_digest = "sha256:" + "e" * 64
    candidate = f"{SITE_REPOSITORY}@{digest}"

    def image(identifier: str, tags: list[str], digests: list[str]) -> dict[str, Any]:
        item = {"Id": f"sha256:{identifier * 64}", "RepoTags": tags, "RepoDigests": digests}
        item["_known_aliases"] = RETENTION.image_aliases(item)
        return item

    candidate_a = image("1", [f"{SITE_REPOSITORY}:release-a", f"{SITE_REPOSITORY}:rollback-a"], [candidate])
    candidate_b = image("2", [f"{SITE_REPOSITORY}:release-b"], [candidate])
    obsolete = image("3", [f"{SITE_REPOSITORY}:old"], [f"{SITE_REPOSITORY}@{obsolete_digest}"])
    foreign = image(
        "4",
        [f"{SITE_REPOSITORY}:shared", "ghcr.io/flidai/leapview:foreign-alias"],
        [f"{SITE_REPOSITORY}@{other_digest}"],
    )
    similar_repository = image(
        "5",
        ["ghcr.io/flidai/leapview-site-evil:tag"],
        ["ghcr.io/flidai/leapview-site-evil@sha256:" + "c" * 64],
    )
    images = [candidate_a, candidate_b, obsolete, foreign, similar_repository]
    state = {
        "caddy": "ghcr.io/caddy:2@sha256:" + "d" * 64,
        "references": {candidate: [("test candidate", candidate)]},
    }
    plan = RETENTION.build_plan(images, [], state, candidate)
    protected_ids = {item["id"] for item in plan["protected"]}
    removal_ids = {item["id"] for item in plan["proposed_removals"]}
    preserved_ids = {item["id"] for item in plan["preserved_ambiguous"]}

    check(candidate_a["Id"] in protected_ids and candidate_b["Id"] in protected_ids,
          "the exact digest did not protect every image carrying that digest across aliases")
    check(removal_ids == {obsolete["Id"]}, "the planner did not isolate the obsolete exact-repository digest")
    check(foreign["Id"] in preserved_ids, "an image with a foreign alias was not preserved")
    check(similar_repository["Id"] in preserved_ids, "a repository with a site-name prefix was misclassified")
    print("PASS pure policy: exact digest, multiple aliases, foreign aliases, and repository boundary")


def distribution_registry_binary() -> Path:
    configured = os.environ.get("REGISTRY_BINARY")
    path = configured or shutil.which("docker-registry")
    check(path is not None, "Distribution Registry is required; install docker-registry or set REGISTRY_BINARY")
    binary = Path(path)
    check(binary.is_file() and os.access(binary, os.X_OK), f"registry binary is not executable: {binary}")
    return binary


def start_distribution_registry(temp_root: Path) -> tuple[subprocess.Popen[bytes], int, Path, Path]:
    binary = distribution_registry_binary()
    registry_version = run([str(binary), "--version"], dict(os.environ), timeout_s=5).strip()
    with socket.socket() as port_socket:
        port_socket.bind(("127.0.0.1", 0))
        port = port_socket.getsockname()[1]
    storage = temp_root / "registry-storage"
    storage.mkdir()
    config = temp_root / "registry.yml"
    config.write_text(
        "version: 0.1\n"
        "log:\n"
        "  accesslog:\n"
        "    disabled: false\n"
        "  level: warn\n"
        "storage:\n"
        "  cache:\n"
        "    blobdescriptor: inmemory\n"
        "  filesystem:\n"
        f"    rootdirectory: {json.dumps(str(storage))}\n"
        "  delete:\n"
        "    enabled: true\n"
        "http:\n"
        f"  addr: 127.0.0.1:{port}\n",
        encoding="utf-8",
    )
    log_path = temp_root / "registry.log"
    with open(log_path, "wb") as log_file:
        process = subprocess.Popen(
            [str(binary), "serve", str(config)],
            stdout=log_file,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    opener = build_opener(ProxyHandler({}))
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if process.poll() is not None:
            break
        try:
            with opener.open(f"http://127.0.0.1:{port}/v2/", timeout=0.5) as response:
                if response.status == 200:
                    print(f"INFO isolated registry: {registry_version}")
                    return process, port, storage, log_path
        except (OSError, URLError):
            time.sleep(0.1)
    stop_private_process(process)
    log_text = log_path.read_text(errors="replace")[-3000:]
    raise RuntimeError(f"private Distribution Registry failed to start\nregistry:\n{log_text}")


def registry_manifest_blobs(port: int, repository: str, digest: str) -> dict[str, Any]:
    opener = build_opener(ProxyHandler({}))
    request = Request(
        f"http://127.0.0.1:{port}/v2/{repository}/manifests/{digest}",
        headers={
            "Accept": ", ".join((
                "application/vnd.docker.distribution.manifest.v2+json",
                "application/vnd.docker.distribution.manifest.list.v2+json",
                "application/vnd.oci.image.manifest.v1+json",
                "application/vnd.oci.image.index.v1+json",
            ))
        },
    )
    with opener.open(request, timeout=5) as response:
        manifest = json.loads(response.read())
    config = manifest.get("config")
    layers = manifest.get("layers")
    check(isinstance(config, dict) and isinstance(config.get("digest"), str),
          f"registry returned a manifest without a config digest: {manifest!r}")
    check(isinstance(layers, list) and layers and all(isinstance(layer, dict) for layer in layers),
          f"registry returned a manifest without layer descriptors: {manifest!r}")
    layer_digests = [layer.get("digest") for layer in layers]
    check(all(isinstance(item, str) for item in layer_digests),
          f"registry returned malformed layer descriptors: {layers!r}")
    return {"manifest": digest, "config": config["digest"], "layers": layer_digests}


def content_blob_path(containerd_root: Path, digest: str) -> Path:
    algorithm, separator, encoded = digest.partition(":")
    check(separator == ":" and algorithm == "sha256" and re.fullmatch(r"[0-9a-f]{64}", encoded) is not None,
          f"unexpected content digest: {digest}")
    return containerd_root / "io.containerd.content.v1.content" / "blobs" / algorithm / encoded


def containerd_content_metrics(containerd_root: Path) -> tuple[int, int]:
    blob_root = containerd_root / "io.containerd.content.v1.content" / "blobs" / "sha256"
    check(blob_root.is_dir(), f"containerd content-store blob directory is missing: {blob_root}")
    blobs = [path for path in blob_root.iterdir() if path.is_file()]
    return len(blobs), sum(path.stat().st_size for path in blobs)


def wait_for_containerd_image_gc(
    containerd_root: Path,
    obsolete: dict[str, Any],
    retained: list[dict[str, Any]],
    shared_layers: list[str],
) -> tuple[int, int]:
    obsolete_unique_layers = [digest for digest in obsolete["layers"] if digest not in shared_layers]
    obsolete_digests = [obsolete["manifest"], obsolete["config"], *obsolete_unique_layers]
    retained_digests = [
        digest
        for item in retained
        for digest in (item["manifest"], item["config"], *item["layers"])
    ]
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        obsolete_present = [digest for digest in obsolete_digests if content_blob_path(containerd_root, digest).exists()]
        retained_missing = [digest for digest in (*retained_digests, *shared_layers)
                            if not content_blob_path(containerd_root, digest).is_file()]
        if not obsolete_present and not retained_missing:
            return containerd_content_metrics(containerd_root)
        time.sleep(0.25)
    raise RuntimeError(
        "containerd content GC did not settle: "
        f"obsolete blobs still present={obsolete_present}, retained/shared blobs missing={retained_missing}"
    )


def private_docker_command(
    temp_root: Path,
    registry_port: int,
    data_root: Path,
    exec_root: Path,
    containerd_root: Path,
    containerd_state: Path,
    containerd_socket: Path,
    docker_socket: Path,
    pidfile: Path,
) -> list[str]:
    daemon_config = temp_root / "daemon.json"
    daemon_config.write_text("{}\n", encoding="utf-8")
    return [
        "/usr/bin/dockerd",
        f"--config-file={daemon_config}",
        f"--host=unix://{docker_socket}",
        f"--data-root={data_root}",
        f"--exec-root={exec_root}",
        f"--pidfile={pidfile}",
        f"--containerd={containerd_socket}",
        # Overlay mounts are unavailable in restricted CI containers; the
        # native containerd snapshotter still exercises the containerd store.
        "--storage-driver=native",
        "--bridge=none",
        "--iptables=false",
        "--ip-forward=false",
        "--ip-masq=false",
        "--userland-proxy=false",
        "--group=docker",
        f"--insecure-registry=127.0.0.1:{registry_port}",
        "--log-level=error",
    ]


def start_daemon(temp_root: Path, registry_port: int) -> tuple[subprocess.Popen[bytes], subprocess.Popen[bytes], str]:
    containerd_socket = temp_root / "containerd/containerd.sock"
    docker_socket = temp_root / "docker.sock"
    data_root = temp_root / "docker-data"
    exec_root = temp_root / "docker-exec"
    containerd_root = temp_root / "containerd/root"
    containerd_state = temp_root / "containerd/state"
    containerd_socket.parent.mkdir(parents=True)
    for path in (data_root, exec_root, containerd_root, containerd_state):
        path.mkdir(parents=True, exist_ok=True)
    containerd_config = temp_root / "containerd/config.toml"
    containerd_config.write_text(
        "version = 3\n"
        f"root = {json.dumps(str(containerd_root))}\n"
        f"state = {json.dumps(str(containerd_state))}\n"
        "[grpc]\n"
        f"uid = {os.getuid()}\n"
        f"gid = {os.getgid()}\n"
        "[ttrpc]\n"
        f"uid = {os.getuid()}\n"
        f"gid = {os.getgid()}\n",
        encoding="utf-8",
    )
    pidfile = temp_root / "dockerd.pid"
    containerd_log = open(temp_root / "containerd.log", "wb")
    dockerd_log = open(temp_root / "dockerd.log", "wb")
    containerd = dockerd = None
    try:
        containerd = subprocess.Popen(
            [
                "/usr/bin/containerd", "--config", str(containerd_config), "--address", str(containerd_socket),
                "--root", str(containerd_root), "--state", str(containerd_state),
            ],
            stdout=containerd_log,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        deadline = time.monotonic() + 15
        while not containerd_socket.exists() and time.monotonic() < deadline:
            if containerd.poll() is not None:
                break
            time.sleep(0.1)
        if not containerd_socket.exists():
            containerd_log.flush()
            containerd_text = (temp_root / "containerd.log").read_text(errors="replace")[-3000:]
            raise RuntimeError(f"private containerd socket did not appear\ncontainerd:\n{containerd_text}")
        dockerd = subprocess.Popen(
            private_docker_command(temp_root, registry_port, data_root, exec_root, containerd_root,
                                   containerd_state, containerd_socket, docker_socket, pidfile),
            stdout=dockerd_log,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        docker_host = f"unix://{docker_socket}"
        docker_config = temp_root / "docker-config"
        docker_config.mkdir()
        (docker_config / "config.json").write_text("{}\n", encoding="utf-8")
        env = dict(os.environ, DOCKER_HOST=docker_host, DOCKER_CONFIG=str(docker_config))
        env.pop("DOCKER_CONTEXT", None)
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            if dockerd.poll() is not None:
                break
            try:
                info = json.loads(run(
                    ["/usr/bin/docker", "info", "--format", "{{json .}}"], env, timeout_s=5
                ))
                version = str(info.get("ServerVersion", ""))
                if version:
                    check(version.startswith("29."), f"expected Docker 29, got {version}")
                    driver = info.get("Driver")
                    status = info.get("DriverStatus") or []
                    driver_type = next(
                        (row[1] for row in status if isinstance(row, list) and len(row) >= 2
                         and row[0] == "driver-type"),
                        None,
                    )
                    check(
                        driver_type == "io.containerd.snapshotter.v1" and driver in {"overlayfs", "native"},
                        f"expected Docker containerd image-store backend, got Driver={driver!r}, DriverStatus={status!r}",
                    )
                    registry_config = info.get("RegistryConfig") or {}
                    index_configs = registry_config.get("IndexConfigs") or {}
                    registry_host = f"127.0.0.1:{registry_port}"
                    insecure_entry = index_configs.get(registry_host)
                    check(
                        isinstance(insecure_entry, dict) and insecure_entry.get("Secure") is False,
                        f"private Docker does not report {registry_host} as insecure: {registry_config!r}",
                    )
                    print(f"PASS backend: Docker {version}, Driver={driver}, {driver_type}")
                    print(f"PASS registry config: {registry_host} is reported insecure")
                    return containerd, dockerd, docker_host
            except RuntimeError:
                time.sleep(0.25)
        dockerd_log.flush()
        containerd_log.flush()
        dockerd_text = (temp_root / "dockerd.log").read_text(errors="replace")[-5000:]
        containerd_text = (temp_root / "containerd.log").read_text(errors="replace")[-3000:]
        raise RuntimeError(f"isolated Docker failed to start\ndockerd:\n{dockerd_text}\ncontainerd:\n{containerd_text}")
    except BaseException:
        stop_private_process(dockerd)
        stop_private_process(containerd)
        raise
    finally:
        containerd_log.close()
        dockerd_log.close()


def tiny_tar(payload: str) -> bytes:
    result = io.BytesIO()
    data = payload.encode()
    with tarfile.open(fileobj=result, mode="w") as archive:
        member = tarfile.TarInfo("fixture.txt")
        member.size = len(data)
        member.mode = 0o600
        member.mtime = 0
        archive.addfile(member, io.BytesIO(data))
    return result.getvalue()


def create_published_fixture(
    tag: str,
    docker_env: dict[str, str],
    registry_repo: str,
    base_image_id: str,
    fixture_root: Path,
) -> tuple[str, str, list[str]]:
    local_tag = f"{registry_repo}:{tag}"
    container_name = f"retention-qual-{tag}"
    run(
        ["/usr/bin/docker", "create", "--name", container_name, "--env", f"RETENTION_FIXTURE={tag}", base_image_id, "/bin/true"],
        docker_env,
    )
    fixture_file = fixture_root / f"retention-fixture-{tag}.txt"
    fixture_file.write_text(f"unique retained-image payload for fixture {tag}\n", encoding="utf-8")
    run(["/usr/bin/docker", "cp", str(fixture_file), f"{container_name}:/retention-cycle.txt"], docker_env)
    run(
        ["/usr/bin/docker", "commit", "--change", f"LABEL retention.fixture={tag}", container_name, local_tag],
        docker_env,
    )
    run(["/usr/bin/docker", "rm", container_name], docker_env)
    push_output = run(["/usr/bin/docker", "push", local_tag], docker_env)
    match = re.search(r"digest: (sha256:[0-9a-f]{64})", push_output)
    check(match is not None, f"registry push did not return a manifest digest for {tag}: {push_output}")
    digest = match.group(1)
    run(["/usr/bin/docker", "image", "rm", local_tag], docker_env)
    run(["/usr/bin/docker", "pull", f"{registry_repo}@{digest}"], docker_env)
    image_id = run(["/usr/bin/docker", "image", "inspect", f"{registry_repo}@{digest}", "--format", "{{.Id}}"], docker_env).strip()
    tags = [f"{registry_repo}:{tag}", f"{registry_repo}:{tag}-alias"]
    for image_tag in tags:
        run(["/usr/bin/docker", "tag", image_id, image_tag], docker_env)
    inspected = json.loads(run(["/usr/bin/docker", "image", "inspect", image_id], docker_env))[0]
    check(f"{registry_repo}@{digest}" in (inspected.get("RepoDigests") or []),
          f"pulled fixture lacks expected RepoDigest: {inspected}")
    check(set(tags).issubset(inspected.get("RepoTags") or []), "fixture aliases were not attached")
    return image_id, digest, tags


def install_docker_adapter(bin_dir: Path, registry_port: int) -> Path:
    wrapper = bin_dir / "docker"
    wrapper.write_text(
        "#!/usr/bin/python3\n"
        "import json, os, re, subprocess, sys\n"
        "real='/usr/bin/docker'\n"
        f"local={json.dumps(REGISTRY_REPOSITORY.format(port=registry_port))}\n"
        f"site={json.dumps(SITE_REPOSITORY)}\n"
        "args=sys.argv[1:]\n"
        "def remap(value, reverse=False):\n"
        "    source, target=(site, local) if reverse else (local, site)\n"
        "    if value == source or value.startswith(source + ':' ) or value.startswith(source + '@'):\n"
        "        return target + value[len(source):]\n"
        "    return value\n"
        "if len(args)>=3 and args[:2]==['image','inspect']:\n"
        "    p=subprocess.run([real,*args],capture_output=True,text=True)\n"
        "    if p.returncode: sys.exit(p.returncode)\n"
        "    try: rows=json.loads(p.stdout)\n"
        "    except Exception: print(p.stdout,end=''); sys.exit(0)\n"
        "    for row in rows:\n"
        "        for key in ('RepoTags','RepoDigests'):\n"
        "            if isinstance(row.get(key),list): row[key]=[remap(v) for v in row[key]]\n"
        "    print(json.dumps(rows,separators=(',',':')))\n"
        "    sys.exit(0)\n"
        "if len(args)>=3 and args[:2]==['image','rm']:\n"
        "    args=[args[0],args[1],*[remap(v,True) for v in args[2:]]]\n"
        "os.execv(real,[real,*args])\n",
        encoding="utf-8",
    )
    wrapper.chmod(0o700)
    return wrapper


def write_site_state(site_root: Path, active_ref: str, previous_ref: str | None) -> None:
    site_root.mkdir(parents=True, exist_ok=True)
    caddy = "ghcr.io/caddy:2@sha256:" + "d" * 64
    (site_root / "deployment.env").write_text(
        f"LEAPVIEW_SITE_IMAGE={active_ref}\nCADDY_IMAGE={caddy}\n", encoding="utf-8"
    )
    (site_root / "deployed-image").write_text(active_ref + "\n", encoding="utf-8")
    if previous_ref:
        (site_root / "previous-image").write_text(previous_ref + "\n", encoding="utf-8")
        (site_root / "retention-first-install").unlink(missing_ok=True)
    else:
        (site_root / "previous-image").unlink(missing_ok=True)
        (site_root / "retention-first-install").write_text(active_ref + "\n", encoding="utf-8")


def helper_call(helper_env: dict[str, str], site_root: Path, storage: Path, operation: str,
                candidate: str | None = None) -> dict[str, Any]:
    command = [
        sys.executable,
        str(HELPER),
        operation,
        "--site-root",
        str(site_root),
        "--storage-path",
        str(storage),
        "--min-free-bytes",
        "0",
    ]
    if candidate:
        command.extend(["--candidate-ref", candidate])
    output = run(command, helper_env)
    return json.loads(output)


def fixture_ids(docker_env: dict[str, str]) -> set[str]:
    output = run(["/usr/bin/docker", "image", "ls", "--all", "--quiet", "--no-trunc"], docker_env)
    return {line.strip() for line in output.splitlines() if line.strip()}


def qualify_docker29() -> None:
    with tempfile.TemporaryDirectory(prefix="leapview-site-retention-qual-") as temp_name:
        temp_root = Path(temp_name)
        registry, port, registry_storage, registry_log = start_distribution_registry(temp_root)
        containerd = dockerd = None
        try:
            containerd, dockerd, docker_host = start_daemon(temp_root, port)
            docker_env = dict(os.environ, DOCKER_HOST=docker_host)
            docker_config = temp_root / "docker-config"
            docker_env["DOCKER_CONFIG"] = str(docker_config)
            docker_env.pop("DOCKER_CONTEXT", None)
            check(not fixture_ids(docker_env), "isolated daemon did not start empty")
            registry_repo = REGISTRY_REPOSITORY.format(port=port)
            foreign_repo = FOREIGN_REPOSITORY.format(port=port)
            base_tag = "retention-qual-base:shared"
            run(
                ["/usr/bin/docker", "import", "-", base_tag],
                docker_env,
                input_bytes=tiny_tar("shared one-file layer used by every test image\n"),
            )
            base_image_id = run(
                ["/usr/bin/docker", "image", "inspect", base_tag, "--format", "{{.Id}}"], docker_env
            ).strip()
            base_layers = json.loads(
                run(["/usr/bin/docker", "image", "inspect", base_image_id], docker_env)
            )[0]["RootFS"]["Layers"]
            site_root = temp_root / "site-root"
            storage = temp_root / "storage"
            storage.mkdir()
            helper_bin = temp_root / "helper-bin"
            helper_bin.mkdir()
            install_docker_adapter(helper_bin, port)
            helper_env = dict(docker_env, PATH=f"{helper_bin}:{os.environ.get('PATH', '')}")

            fixture_refs: list[tuple[str, str, list[str]]] = []
            fixture_content: list[dict[str, Any]] = []
            shared_layer_digests: list[str] | None = None
            steady_content_metrics: tuple[int, int] | None = None
            containerd_root = temp_root / "containerd/root"
            for index in range(11):
                tag = f"cycle-{index:02d}"
                image_id, digest, tags = create_published_fixture(
                    tag, docker_env, registry_repo, base_image_id, temp_root
                )
                fixture_refs.append((image_id, digest, tags))
                content_refs = registry_manifest_blobs(port, "site", digest)
                if shared_layer_digests is None:
                    shared_layer_digests = content_refs["layers"][:len(base_layers)]
                else:
                    check(content_refs["layers"][:len(base_layers)] == shared_layer_digests,
                          f"cycle {index}: registry manifest stopped sharing the base layer blobs")
                unique_layers = content_refs["layers"][len(base_layers):]
                check(len(unique_layers) == 1, f"cycle {index}: expected exactly one unique file layer")
                check(unique_layers[0] not in (shared_layer_digests or []),
                      f"cycle {index}: unique file layer unexpectedly aliases a shared layer")
                check(all(unique_layers[0] != item["layers"][-1] for item in fixture_content),
                      f"cycle {index}: per-cycle file layer was not unique")
                for content_digest in [content_refs["manifest"], content_refs["config"], *content_refs["layers"]]:
                    check(content_blob_path(containerd_root, content_digest).is_file(),
                          f"cycle {index}: expected containerd content blob is missing: {content_digest}")
                fixture_content.append(content_refs)
                image_layers = json.loads(
                    run(["/usr/bin/docker", "image", "inspect", image_id], docker_env)
                )[0]["RootFS"]["Layers"]
                check(
                    len(image_layers) == len(base_layers) + 1 and image_layers[:len(base_layers)] == base_layers,
                    f"cycle {index}: test image stopped sharing its base layer or lost its unique payload layer",
                )
                if index == 0:
                    write_site_state(site_root, f"{SITE_REPOSITORY}@{digest}", None)
                else:
                    previous_digest = fixture_refs[index - 1][1]
                    active_ref = f"{SITE_REPOSITORY}@{digest}"
                    previous_ref = f"{SITE_REPOSITORY}@{previous_digest}"
                    write_site_state(site_root, active_ref, previous_ref)
                    plan = helper_call(helper_env, site_root, storage, "plan", active_ref)
                    active_id, previous_id = image_id, fixture_refs[index - 1][0]
                    plan_protected = {item["id"] for item in plan["protected"]}
                    check(active_id in plan_protected and previous_id in plan_protected,
                          f"cycle {index}: active or rollback digest was not protected")
                    expected_old = {fixture_refs[index - 2][0]} if index >= 2 else set()
                    planned_old = {item["id"] for item in plan["proposed_removals"]}
                    check(planned_old == expected_old, f"cycle {index}: unexpected plan removals {planned_old ^ expected_old}")
                    applied = helper_call(helper_env, site_root, storage, "apply", active_ref)
                    expected_refs = [alias for item in applied["proposed_removals"] for alias in item["aliases"]]
                    handled_refs = (
                        applied.get("removed_references", [])
                        + applied.get("implicitly_removed_references", [])
                        + applied.get("already_absent_references", [])
                    )
                    check(
                        len(handled_refs) == len(expected_refs) and set(handled_refs) == set(expected_refs),
                        f"cycle {index}: planned aliases were not accounted for: {set(expected_refs) ^ set(handled_refs)}",
                    )
                    if index >= 2:
                        obsolete_digest = f"{SITE_REPOSITORY}@{fixture_refs[index - 2][1]}"
                        check(
                            obsolete_digest in applied.get("implicitly_removed_references", []),
                            f"cycle {index}: Docker did not account for digest removal through the prior tag deletions",
                        )
                    remaining = fixture_ids(docker_env)
                    expected_remaining = {base_image_id, *(entry[0] for entry in fixture_refs[index - 1 : index + 1])}
                    check(remaining == expected_remaining,
                          f"cycle {index}: image inventory did not converge to active+rollback: {remaining ^ expected_remaining}")
                    if index >= 2:
                        content_metrics = wait_for_containerd_image_gc(
                            containerd_root,
                            fixture_content[index - 2],
                            [fixture_content[index - 1], fixture_content[index]],
                            shared_layer_digests or [],
                        )
                        if steady_content_metrics is None:
                            steady_content_metrics = content_metrics
                        check(content_metrics[0] == steady_content_metrics[0],
                              f"cycle {index}: containerd content blob count grew after stale-image GC: "
                              f"{content_metrics[0]} vs settled baseline {steady_content_metrics[0]}")
                        print(
                            f"PASS containerd content GC cycle {index}: obsolete manifest/config/unique layer absent, "
                            f"shared layer retained, {content_metrics[0]} blobs / {content_metrics[1]} bytes"
                        )

            print("PASS Docker 29: ten deployment cycles retain exactly active+rollback and remove every tag/digest alias")

            foreign_id, _foreign_digest, foreign_tags = create_published_fixture(
                "foreign-boundary", docker_env, registry_repo, base_image_id, temp_root
            )
            foreign_local_tag = f"{foreign_repo}:independent"
            run(["/usr/bin/docker", "tag", foreign_id, foreign_local_tag], docker_env)
            foreign_push = run(["/usr/bin/docker", "push", foreign_local_tag], docker_env)
            foreign_match = re.search(r"digest: (sha256:[0-9a-f]{64})", foreign_push)
            check(foreign_match is not None, "foreign alias push did not return a digest")
            run(["/usr/bin/docker", "image", "rm", foreign_local_tag], docker_env)
            run(["/usr/bin/docker", "pull", f"{foreign_repo}@{foreign_match.group(1)}"], docker_env)
            for tag in foreign_tags:
                run(["/usr/bin/docker", "tag", foreign_id, tag], docker_env)
            write_site_state(
                site_root,
                f"{SITE_REPOSITORY}@{fixture_refs[-1][1]}",
                f"{SITE_REPOSITORY}@{fixture_refs[-2][1]}",
            )
            foreign_plan = helper_call(
                helper_env,
                site_root,
                storage,
                "plan",
                f"{SITE_REPOSITORY}@{fixture_refs[-1][1]}",
            )
            foreign_aliases = set()
            foreign_inspect = json.loads(run(["/usr/bin/docker", "image", "inspect", foreign_id], docker_env))[0]
            foreign_aliases.update(foreign_inspect.get("RepoDigests") or [])
            foreign_aliases.update(foreign_inspect.get("RepoTags") or [])
            check(any(alias.startswith(foreign_repo) for alias in foreign_aliases),
                  "fixture did not expose the foreign repository reference")
            planned_ids = {item["id"] for item in foreign_plan["proposed_removals"]}
            preserved = {item["id"]: item for item in foreign_plan["preserved_ambiguous"]}
            check(foreign_id not in planned_ids and foreign_id in preserved,
                  "helper planned deletion of an image carrying a foreign repository reference")
            check("foreign or unrecognized alias" in preserved[foreign_id]["reason"],
                  "foreign-reference preservation did not identify the ambiguity")
            print("PASS Docker 29: exact site image with a foreign repository alias is preserved")

            stop_private_process(registry)
            registry_access = registry_log.read_text(errors="replace")
            seen_repositories = set(REGISTRY_REQUEST.findall(registry_access))
            unexpected_repositories = seen_repositories - {"site", "foreign"}
            check(not unexpected_repositories, f"registry saw unexpected repositories: {unexpected_repositories}")
            check(seen_repositories == {"site", "foreign"},
                  f"registry traffic did not cover exactly the local test repositories: {seen_repositories}")
            stored_repositories = registry_storage / "docker/registry/v2/repositories"
            check({path.name for path in stored_repositories.iterdir()} == {"site", "foreign"},
                  "registry storage contains an unexpected repository")
            print("PASS loopback registry boundary: observed requests and stored repositories were limited to local fixtures")
        finally:
            stop_private_process(dockerd)
            stop_private_process(containerd)
            stop_private_process(registry)


def main() -> int:
    check(os.geteuid() == 0, "run as root (for example: sudo -n python3 -B scripts/qualify_site_image_retention_docker29.py)")
    check(HELPER.is_file(), f"retention helper is missing: {HELPER}")
    check(shutil.which("dockerd") == "/usr/bin/dockerd", "expected local /usr/bin/dockerd")
    check(shutil.which("containerd") == "/usr/bin/containerd", "expected local /usr/bin/containerd")
    distribution_registry_binary()
    qualify_pure_policy()
    qualify_docker29()
    print("Site image retention qualification passed.")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        raise SystemExit(1)
