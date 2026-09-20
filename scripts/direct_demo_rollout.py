#!/usr/bin/env python3
"""Preflight and safely roll out one admitted hosted-demo image.

The script runs on the demo host through the pinned SSH wrapper. It backs up
both PostgreSQL databases and the runtime home before applying the canonical
database initialization boundary, and restores all three plus the service unit
if the cutover does not become healthy.
"""

import hashlib
import json
import os
from pathlib import Path
import pwd
import shutil
import subprocess
import sys
import time
import urllib.parse
import urllib.request


REVISION = os.environ["DIRECT_DEMO_REVISION"]
IMAGE = os.environ["DIRECT_DEMO_IMAGE"]
PREDECESSOR_SCHEMA = int(os.environ.get("DIRECT_DEMO_PREDECESSOR_SCHEMA", "22"))
EXPECTED_SCHEMA = int(os.environ.get("DIRECT_DEMO_EXPECTED_SCHEMA", "23"))
PUBLIC_URL = os.environ.get("DIRECT_DEMO_PUBLIC_URL", "https://demo.leapview.dev").rstrip("/")
RELEASE = Path("/opt/leapview-demo/releases") / REVISION
SERVICE = "leapview-demo-current.service"
UNIT = Path("/etc/systemd/system") / SERVICE


def output(*args: str) -> str:
    return subprocess.check_output(args, text=True).strip()


def database_container() -> str:
    names = [name for name in output("docker", "ps", "--format", "{{.Names}}").splitlines() if name.endswith("-demo-current-postgres-1")]
    if len(names) != 1:
        raise RuntimeError(f"expected one hosted-demo PostgreSQL container, found {len(names)}")
    return names[0]


def sql(container: str, query: str) -> str:
    return output(
        "docker", "exec", container, "sh", "-c",
        'exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d leapview_control -Atc "$1"',
        "query", query,
    )


def active_delivery_state(container: str) -> list[dict[str, str]]:
    result = sql(
        container,
        "SELECT COALESCE(json_agg(json_build_object('target', p.target_id, 'generation', p.generation_id::text, 'project', t.project_id, 'environment', t.environment) ORDER BY p.target_id), '[]'::json)::text FROM delivery.delivery_active_pointer p JOIN delivery.delivery_generation g ON g.generation_id = p.generation_id JOIN delivery.delivery_target t ON t.target_id = p.target_id",
    )
    state = json.loads(result)
    if not state:
        raise RuntimeError("hosted-demo has no active delivery generation")
    return state


def ready() -> bool:
    try:
        with urllib.request.urlopen("http://127.0.0.1:8132/readyz", timeout=3) as response:
            return response.status == 200 and json.load(response).get("status") == "ready"
    except Exception:
        return False


def await_ready() -> None:
    initial_restarts = int(output("systemctl", "show", SERVICE, "--property=NRestarts", "--value") or "0")
    for _ in range(60):
        if ready():
            return
        restarts = int(output("systemctl", "show", SERVICE, "--property=NRestarts", "--value") or "0")
        if restarts - initial_restarts >= 3:
            raise RuntimeError("runtime repeatedly exited during startup")
        time.sleep(2)
    raise RuntimeError("runtime did not become ready within the cutover window")


def write_private(path: Path, value: str) -> None:
    path.write_text(value)
    path.chmod(0o600)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def process_environment(pid: str) -> dict[str, str]:
    return {
        item.decode().split("=", 1)[0]: item.decode().split("=", 1)[1]
        for item in Path("/proc", pid, "environ").read_bytes().split(b"\0")
        if b"=" in item
    }


def verify_agent_environment(environment: dict[str, str]) -> tuple[bool, bool]:
    key_configured = bool(environment.get("LEAPVIEW_AGENT_API_KEY", "").strip())
    model_configured = bool(environment.get("LEAPVIEW_AGENT_MODEL", "").strip())
    print(
        "Agent configuration: "
        f"api_key={'configured' if key_configured else 'missing'} "
        f"model={'configured' if model_configured else 'missing'}",
        flush=True,
    )
    return key_configured, model_configured


def public_checks() -> None:
    for path in ("/readyz", "/login"):
        with urllib.request.urlopen(PUBLIC_URL + path, timeout=15) as response:
            if response.status != 200:
                raise RuntimeError(f"hosted-demo {path} returned HTTP {response.status}")


def main() -> None:
    if sys.argv[1:] not in (["--check"], ["--apply"]):
        raise SystemExit("Expected --check or --apply")
    if not REVISION or len(REVISION) != 40 or any(char not in "0123456789abcdef" for char in REVISION):
        raise SystemExit("DIRECT_DEMO_REVISION must be a full lowercase Git commit identity")
    if not IMAGE.startswith("ghcr.io/flidai/leapview@sha256:"):
        raise SystemExit("DIRECT_DEMO_IMAGE must be an immutable LeapView image")

    if output("systemctl", "is-active", SERVICE) != "active":
        raise RuntimeError("hosted-demo service is not active")
    pid = output("systemctl", "show", SERVICE, "--property=MainPID", "--value")
    args = [item.decode() for item in Path("/proc", pid, "cmdline").read_bytes().split(b"\0") if item]
    if args[1:] != ["serve", "--production"]:
        raise RuntimeError("unexpected hosted-demo service arguments")
    runtime_env = process_environment(pid)
    home_value = runtime_env.get("LEAPVIEW_HOME", "")
    if not home_value.startswith("/"):
        raise RuntimeError("hosted-demo LEAPVIEW_HOME must be an absolute path")
    home_path = Path(home_value)
    if not home_path.exists():
        raise RuntimeError("hosted-demo runtime home does not exist")
    if not ready():
        raise RuntimeError("hosted-demo predecessor is not ready")

    previous = json.loads(output(f"/proc/{pid}/exe", "version", "--json"))
    if previous.get("revision") == REVISION or previous.get("dirty") is not False:
        raise RuntimeError("active runtime is not a clean predecessor of the direct rollout")
    predecessor_release = Path("/opt/leapview-demo/releases") / previous["revision"]
    predecessor_checksum_path = predecessor_release / "leapview.sha256"
    if not predecessor_checksum_path.exists() or sha256(Path("/proc", pid, "exe")) != predecessor_checksum_path.read_text().split()[0]:
        raise RuntimeError("active runtime binary does not match its recorded release checksum")

    if not (RELEASE / "leapview").is_file() or not (RELEASE / "immutable-image.txt").is_file() or not (RELEASE / "leapview.sha256").is_file():
        raise RuntimeError("direct-demo image is not staged completely")
    if (RELEASE / "immutable-image.txt").read_text().strip() != IMAGE:
        raise RuntimeError("staged image reference differs from admitted digest")
    identity = json.loads(output(str(RELEASE / "leapview"), "version", "--json"))
    if identity.get("revision") != REVISION or identity.get("dirty") is not False or sha256(RELEASE / "leapview") != (RELEASE / "leapview.sha256").read_text().split()[0]:
        raise RuntimeError("staged release identity or checksum is invalid")

    container = database_container()
    delivery_before = active_delivery_state(container)
    schema = int(sql(container, "SELECT max(version_id) FROM public.goose_db_version WHERE is_applied"))
    if schema != PREDECESSOR_SCHEMA:
        raise RuntimeError(f"expected predecessor schema {PREDECESSOR_SCHEMA}, found {schema}")
    runtime_url = urllib.parse.urlsplit(runtime_env["LEAPVIEW_POSTGRES_CONTROL_URL"])
    operation_env = runtime_env.copy()
    migrator_env_file = Path(os.environ.get("DIRECT_DEMO_MIGRATOR_ENV_FILE", "/tmp/leapview-main/.tmp/postgres-demo-current.env"))
    if migrator_env_file.exists():
        for line in migrator_env_file.read_text().splitlines():
            key, separator, value = line.partition("=")
            if separator and key in ("LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL", "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL"):
                operation_env[key] = value
    if "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL" not in operation_env:
        container_env = dict(
            item.split("=", 1)
            for item in json.loads(output("docker", "inspect", "--format", "{{json .Config.Env}}", container))
            if "=" in item
        )
        password = container_env["LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD"]
        authority = "leapview_control_migrator:" + urllib.parse.quote(password, safe="") + "@" + runtime_url.netloc.rsplit("@", 1)[-1]
        operation_env["LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL"] = urllib.parse.urlunsplit(runtime_url._replace(netloc=authority))
    migration_url = urllib.parse.urlsplit(operation_env["LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL"])
    if (runtime_url.hostname, runtime_url.port, runtime_url.path) != (migration_url.hostname, migration_url.port, migration_url.path) or runtime_url.username == migration_url.username:
        raise RuntimeError("database migration URL is not bound to the active control database")
    ports = output("docker", "port", container, "5432/tcp").splitlines()
    if not any(port.endswith(":" + str(runtime_url.port)) for port in ports):
        raise RuntimeError("database migration target is not the active PostgreSQL container")
    database_size = int(sql(container, "SELECT sum(pg_database_size(oid)) FROM pg_database WHERE datname IN ('leapview_control','leapview_ducklake')"))
    required = 2 * (int(output("du", "-sb", str(home_path)).split()[0]) + database_size) + 512 * 1024 * 1024
    if shutil.disk_usage("/opt").free <= required:
        raise RuntimeError("insufficient /opt space for a complete rollback backup")
    agent_before = verify_agent_environment(runtime_env)
    print(f"Preflight passed: clean predecessor {previous['revision']}, schema {schema}, rollback budget {required} bytes", flush=True)
    if sys.argv[1] == "--check":
        return

    backup = Path("/opt/leapview-demo/rollbacks") / (time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()) + "-" + REVISION[:12])
    backup.mkdir(parents=True, mode=0o700)
    original_fragment = Path(output("systemctl", "show", SERVICE, "--property=FragmentPath", "--value"))
    original_unit = original_fragment.read_text()
    if original_fragment not in (Path("/run/systemd/transient") / SERVICE, UNIT):
        raise RuntimeError("unexpected systemd unit owner")
    write_private(backup / "original.service", original_unit)
    write_private(backup / "predecessor.json", json.dumps(previous))
    service_stopped = False
    migration_started = False
    try:
        service_stopped = True
        subprocess.run(["systemctl", "stop", SERVICE], check=True)
        for database in ("leapview_control", "leapview_ducklake"):
            with (backup / (database + ".dump")).open("wb") as handle:
                subprocess.run(["docker", "exec", container, "sh", "-c", 'exec pg_dump -U "$POSTGRES_USER" --format=custom --dbname="$1"', "backup", database], stdout=handle, check=True)
            with (backup / (database + ".dump")).open("rb") as handle:
                subprocess.run(["docker", "exec", "-i", container, "pg_restore", "--list"], stdin=handle, stdout=subprocess.DEVNULL, check=True)
        subprocess.run(["tar", "-cpf", str(backup / "home.tar"), "-C", str(home_path.parent), home_path.name], check=True)
        migration_started = True
        with (backup / "baseline-output.json").open("wb") as stdout, (backup / "baseline-error.log").open("wb") as stderr:
            migration = subprocess.run([str(RELEASE / "leapview"), "admin", "initialize", "--format", "json"], cwd=RELEASE, env=operation_env, stdout=stdout, stderr=stderr)
        if migration.returncode and "already initialized" not in (backup / "baseline-error.log").read_text():
            raise RuntimeError("canonical database initialization failed; private diagnostics retained in rollback backup")
        schema = int(sql(container, "SELECT max(version_id) FROM public.goose_db_version WHERE is_applied"))
        if schema != EXPECTED_SCHEMA:
            raise RuntimeError(f"expected deployed schema {EXPECTED_SCHEMA}, found {schema}")

        environment_file = RELEASE / "runtime.env"
        environment_lines = []
        for name, value in sorted(runtime_env.items()):
            if name.startswith("LEAPVIEW_") or name in ("HOME", "PATH", "USER", "LOGNAME", "LANG", "LC_ALL", "SHELL", "TMPDIR"):
                escaped = value.replace("\\", "\\\\").replace('"', '\\"').replace("$", "\\$").replace("`", "\\`")
                environment_lines.append(name + '=\"' + escaped + '\"')
        write_private(environment_file, "\n".join(environment_lines) + "\n")
        base_unit = "\n".join(line for line in original_unit.splitlines() if not line.startswith(("EnvironmentFile=", "ExecStart=", "WorkingDirectory=")))
        unit = base_unit + "\n\n[Service]\nEnvironmentFile=" + str(environment_file) + "\nExecStart=" + str(RELEASE / "leapview") + " serve --production\nWorkingDirectory=" + str(RELEASE) + "\n\n[Install]\nWantedBy=multi-user.target\n"
        write_private(UNIT, unit)
        subprocess.run(["systemctl", "daemon-reload"], check=True)
        subprocess.run(["systemctl", "reset-failed", SERVICE], check=False)
        subprocess.run(["systemctl", "start", SERVICE], check=True)
        await_ready()
        new_pid = output("systemctl", "show", SERVICE, "--property=MainPID", "--value")
        live = json.loads(output(f"/proc/{new_pid}/exe", "version", "--json"))
        if live.get("revision") != REVISION or live.get("dirty") is not False:
            raise RuntimeError("cutover process did not report the admitted clean revision")
        agent_after = verify_agent_environment(process_environment(new_pid))
        if agent_after != agent_before:
            raise RuntimeError("agent configuration changed during runtime cutover")
        delivery_after = active_delivery_state(container)
        if delivery_after != delivery_before:
            raise RuntimeError("active CFO serving-state identity changed during runtime cutover")
        print(f"Preserved active serving state: {len(delivery_after)} target(s), project identities unchanged", flush=True)
        public_checks()
        subprocess.run(["systemctl", "enable", SERVICE], check=True)
        write_private(backup / "rollout-success.json", json.dumps({"revision": REVISION, "image": IMAGE, "schema": EXPECTED_SCHEMA}))
        print(f"Deployed {REVISION}; health and login checks passed; agent configuration status preserved; rollback backup: {backup}", flush=True)
    except BaseException as rollout_error:
        if service_stopped:
            subprocess.run(["systemctl", "stop", SERVICE], check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            rollback_errors = []
            if migration_started:
                for database in ("leapview_control", "leapview_ducklake"):
                    try:
                        with (backup / (database + ".dump")).open("rb") as handle:
                            subprocess.run(["docker", "exec", "-i", container, "sh", "-c", 'exec pg_restore -U "$POSTGRES_USER" --clean --if-exists --create --exit-on-error --dbname=template1'], stdin=handle, check=True)
                    except BaseException as error:
                        rollback_errors.append(f"{database} restore: {error}")
                try:
                    if home_path.exists():
                        home_path.rename(backup / "failed-home")
                    subprocess.run(["tar", "-xpf", str(backup / "home.tar"), "-C", str(home_path.parent)], check=True)
                except BaseException as error:
                    rollback_errors.append(f"home restore: {error}")
            try:
                write_private(UNIT, original_unit)
                subprocess.run(["systemctl", "daemon-reload"], check=True)
            except BaseException as error:
                rollback_errors.append(f"unit restore: {error}")
            try:
                subprocess.run(["systemctl", "reset-failed", SERVICE], check=False)
                subprocess.run(["systemctl", "start", SERVICE], check=True)
                await_ready()
            except BaseException as error:
                rollback_errors.append(f"predecessor restart: {error}")
            if rollback_errors:
                raise RuntimeError("rollout failed and rollback was incomplete: " + "; ".join(rollback_errors)) from rollout_error
            print("Rollout failed; predecessor, database state, home, and service unit restored", flush=True)
        raise


if __name__ == "__main__":
    main()
