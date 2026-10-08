"""Prepare real TLS PostgreSQL on the qualification-only support daemon.

The caller owns verified namespace isolation, DNS, pre-pulled images and daemon
cleanup. Only the returned ``evidence`` is suitable for artifact retention;
``urls``, ``credentials`` and ``privateFiles`` remain private operator inputs.
"""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shlex
import stat
import sys

sys.dont_write_bytecode = True
sys.path.insert(0, str(Path(__file__).resolve().parents[4] / "scripts"))
import nix_compose_host_guest as guest


class SupportError(RuntimeError):
    """A bounded, credential-free support preparation failure."""


def native_architecture():
    native = platform.machine()
    for architecture, machine in guest.ARCHITECTURES.items():
        if native == machine:
            return architecture
    raise SupportError("unsupported native PostgreSQL qualification architecture")


def _canonical_path(value, label):
    path = Path(value)
    if not path.is_absolute() or path != path.resolve() or any(char in str(path) for char in "\x00\r\n,:"):
        raise SupportError(f"{label} must be a canonical absolute path")
    return path


def prepare_postgres(*, support_docker, run, root: Path, source: Path,
                     image: str, home_base: Path,
                     support_socket: Path = Path("/run/support-docker.sock")) -> dict:
    root = _canonical_path(root, "support root")
    source = _canonical_path(source, "source root")
    home_base = _canonical_path(home_base, "application home base")
    support_socket = _canonical_path(support_socket, "support Docker socket")
    if image != guest._locked_postgres_image(source):
        raise SupportError("PostgreSQL image must match the source-locked immutable reference")
    for directory in (root, home_base):
        info = directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o077:
            raise SupportError("support and application directories must already be private")

    # Identity validation occurs before credentials or a database are created.
    try:
        inspected = support_docker("image", "inspect", image, timeout=30)
        metadata = json.loads(inspected.stdout)
        expected_digest = guest._postgres_repo_digest(image)
        if (inspected.returncode or not isinstance(metadata, list) or len(metadata) != 1
                or not isinstance(metadata[0], dict)):
            raise ValueError("invalid inventory")
        metadata = metadata[0]
        image_id = metadata.get("Id", "")
        repo_digests = metadata.get("RepoDigests")
        native_platform = "linux/" + native_architecture()
        actual_platform = str(metadata.get("Os")) + "/" + str(metadata.get("Architecture"))
        if (not isinstance(image_id, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", image_id)
                or not isinstance(repo_digests, list) or expected_digest not in repo_digests
                or any(not isinstance(item, str) for item in repo_digests)
                or actual_platform != native_platform):
            raise ValueError("mismatched inventory")
    except Exception:
        raise SupportError("retained PostgreSQL image identity is invalid") from None

    fixture = root / "postgres"
    try:
        fixture.mkdir(mode=0o700)
    except OSError:
        raise SupportError("PostgreSQL fixture must use a fresh private directory") from None
    tls = fixture / "tls"
    tls.mkdir(mode=0o700)
    data = fixture / "data"
    data.mkdir(mode=0o700)
    environment = fixture / "postgres.env"
    name = "leapview-managed-pg-" + secrets.token_hex(8)
    start_attempted = False
    stage = "private preparation"
    try:
        credentials = guest._postgres_fixture_credentials()
        urls = guest._postgres_connection_urls(credentials)
        with environment.open("xb") as output:
            os.fchmod(output.fileno(), 0o600)
            output.write(guest._postgres_fixture_environment(credentials))
        init_bytes = guest._read(source / "deploy/postgres/init.sh", "canonical PostgreSQL init", 2 * 1024**2)
        init_script = fixture / "postgres-init.sh"
        init_script.write_bytes(init_bytes)
        init_script.chmod(0o644)
        ca_key, ca_cert = tls / "ca.key", tls / "ca.pem"
        server_key, server_csr, server_cert = tls / "server.key", tls / "server.csr", tls / "server.pem"
        extension = tls / "server.ext"
        extension.write_text("subjectAltName=DNS:postgres\nextendedKeyUsage=serverAuth\n")
        stage = "TLS certificate preparation"
        run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
            "-keyout", str(ca_key), "-out", str(ca_cert),
            "-subj", "/CN=leapview-qualification-ca", "-days", "2",
            "-addext", "basicConstraints=critical,CA:TRUE",
            "-addext", "keyUsage=critical,keyCertSign,cRLSign", timeout=120)
        run("openssl", "req", "-newkey", "rsa:2048", "-nodes",
            "-keyout", str(server_key), "-out", str(server_csr),
            "-subj", "/CN=postgres", "-addext", "subjectAltName=DNS:postgres", timeout=120)
        run("openssl", "x509", "-req", "-in", str(server_csr), "-CA", str(ca_cert),
            "-CAkey", str(ca_key), "-CAcreateserial", "-out", str(server_cert),
            "-days", "2", "-extfile", str(extension), timeout=120)
        for path in (ca_key, server_csr, tls / "ca.srl"):
            path.unlink(missing_ok=True)
        server_key.chmod(0o600)
        for path in (ca_cert, server_cert, extension):
            path.chmod(0o644)

        stage = "container startup"
        start_attempted = True
        # Host networking exists only within the caller's isolated namespace.
        # Restrict the database listener to the application's private gateway.
        support_docker(
            "run", "--detach", "--pull=never", "--name", name, "--network", "host", "--user", "0:0",
            "--env-file", str(environment),
            "--volume", str(init_script) + ":/docker-entrypoint-initdb.d/10-leapview-roles.sh:ro",
            "--volume", str(ca_cert) + ":/run/secrets/leapview-postgres-ca.pem:ro",
            "--volume", str(server_cert) + ":/run/secrets/leapview-postgres-server.pem:ro",
            "--volume", str(server_key) + ":/run/secrets/leapview-postgres-server.key:ro",
            "--volume", str(home_base) + ":/var/lib/leapview",
            "--volume", str(data) + ":/var/lib/postgresql",
            "--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777,size=64m",
            "--entrypoint", "sh", image, "-ec",
            # The official entrypoint fixes PGDATA ownership and then reexecs
            # as postgres. A fresh 0700 bind parent also needs that owner so
            # the unprivileged reexec can traverse to PostgreSQL 18's PGDATA.
            "set -eu\nchown postgres:postgres /var/lib/postgresql\n" +
            guest._postgres_tls_entrypoint_script() + " -c listen_addresses=172.30.0.1",
            timeout=120,
        )
        environment.unlink()
        stage = "TLS runtime-role readiness"
        docker_host = "DOCKER_HOST=unix://" + str(support_socket)
        probe = guest._postgres_readiness_wait_command(name, shlex.quote(docker_host))
        # The outer environment also covers nested Docker execs in the shared
        # two-role probe; none may accidentally select the application daemon.
        result = run("env", "-u", "DOCKER_CONTEXT", "-u", "DOCKER_TLS_VERIFY",
                     docker_host, "sh", "-ec", probe, timeout=150, check=False)
        expected = list(guest.TLS_ROLE_EXPECTATIONS.values())
        if result.returncode or result.stdout.strip().splitlines() != expected:
            raise SupportError("PostgreSQL TLS runtime-role readiness failed")
        evidence = {
            "image": image, "imageID": image_id, "platform": actual_platform,
            "repoDigests": repo_digests, "container": name,
            "initScriptSHA256": "sha256:" + hashlib.sha256(init_bytes).hexdigest(),
            "tlsRoleProbes": expected, "listenAddress": "172.30.0.1",
            "environmentFileRemoved": not environment.exists(),
        }
        return {"container": name, "urls": urls, "credentials": credentials,
                "privateFiles": {"data": str(data), "tls": str(tls), "initScript": str(init_script)},
                "evidence": evidence}
    except Exception:
        if start_attempted:
            try:
                support_docker("container", "rm", "--force", name, timeout=30, check=False)
            except Exception:
                pass  # The outer fixture still owns final daemon cleanup.
        raise SupportError("PostgreSQL support " + stage + " failed") from None
    finally:
        environment.unlink(missing_ok=True)
        for path in (tls / "ca.key", tls / "server.csr", tls / "ca.srl"):
            path.unlink(missing_ok=True)
