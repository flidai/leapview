#!/usr/bin/env python3
"""Provision one fresh native managed PostgreSQL 18 cluster using peer authority."""

import argparse
import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import stat
import subprocess


PASSWORD_ROLES = (
    "control_runtime", "control_migrator", "control_maintenance",
    "control_upgrade_coordinator", "ducklake_runtime", "ducklake_migrator",
    "ducklake_maintenance",
)
NONLOGIN_ROLES = ("control_owner", "ducklake_owner", "control_readonly", "control_backup")


class BootstrapFailure(ValueError):
    pass


def private_passwords(directory):
    directory = Path(directory)
    if not directory.is_absolute() or directory.resolve() != directory:
        raise BootstrapFailure("credential directory")
    info = directory.stat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
        raise BootstrapFailure("credential directory")
    passwords = {}
    for role in PASSWORD_ROLES:
        path = directory / (role.replace("_", "-") + "-password")
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, "rb") as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077 or info.st_nlink != 1:
                raise BootstrapFailure("credential file")
            raw = stream.read(130)
        password = raw.removesuffix(b"\n")
        if re.fullmatch(rb"[A-Za-z0-9_-]{32,128}", password) is None:
            raise BootstrapFailure("credential value")
        passwords[role] = password
    if len(set(passwords.values())) != len(passwords):
        raise BootstrapFailure("distinct credentials")
    return passwords


def scram(password):
    """Only salted PostgreSQL verifiers enter SQL; plaintext stays in memory."""
    salt = os.urandom(16)
    salted = hashlib.pbkdf2_hmac("sha256", password, salt, 4096)
    stored = hashlib.sha256(hmac.digest(salted, b"Client Key", "sha256")).digest()
    server = hmac.digest(salted, b"Server Key", "sha256")
    encode = lambda value: base64.b64encode(value).decode("ascii")
    return "SCRAM-SHA-256$4096:" + encode(salt) + "$" + encode(stored) + ":" + encode(server)


def bootstrap_sql(passwords, system_identifier):
    # Serialize the fresh preflight and role transaction. After roles commit,
    # another invocation already fails freshness. An interruption then leaves
    # a non-fresh cluster, which this command refuses rather than reconciling.
    lines = ["""
SET log_statement = 'none';
SET log_min_duration_statement = -1;
SET log_min_duration_sample = -1;
SET log_duration = off;
SET log_min_error_statement = 'panic';
SET statement_timeout = '60s';
SET lock_timeout = '10s';
SELECT pg_advisory_lock(716114892, 180);
BEGIN;
DO $preflight$
BEGIN
    IF current_user <> 'postgres' OR session_user <> 'postgres'
       OR system_user IS NULL OR system_user NOT LIKE 'peer:%' OR inet_client_addr() IS NOT NULL
       OR NOT (SELECT rolsuper FROM pg_roles WHERE rolname=current_user)
       OR current_setting('server_version_num')::int NOT BETWEEN 180000 AND 189999
       OR (SELECT system_identifier::text FROM pg_control_system()) <> '""" + system_identifier + """'
       OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname <> 'postgres' AND left(rolname,3) <> 'pg_')
       OR EXISTS (SELECT 1 FROM pg_database WHERE datname NOT IN ('postgres','template0','template1'))
       OR EXISTS (SELECT 1 FROM pg_namespace WHERE nspname NOT IN ('public','information_schema') AND left(nspname,3) <> 'pg_')
       OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public')
       OR EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public')
       OR EXISTS (SELECT 1 FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname='public')
       OR EXISTS (SELECT 1 FROM pg_extension WHERE extname <> 'plpgsql') THEN
        RAISE EXCEPTION 'fresh enrolled PostgreSQL 18 peer authority required';
    END IF;
END
$preflight$;
"""]
    attributes = "NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS"
    for role in NONLOGIN_ROLES:
        lines.append("CREATE ROLE leapview_" + role + " NOLOGIN " + attributes + ";")
    for role, password in passwords.items():
        lines.append("CREATE ROLE leapview_" + role + " LOGIN " + attributes + " PASSWORD '" + scram(password) + "';")
    lines.append(r"""
GRANT leapview_control_owner TO leapview_control_migrator;
GRANT leapview_ducklake_owner TO leapview_ducklake_migrator;
COMMIT;
CREATE DATABASE leapview_control OWNER leapview_control_owner TEMPLATE template0;
CREATE DATABASE leapview_ducklake OWNER leapview_ducklake_owner TEMPLATE template0;
REVOKE ALL ON DATABASE postgres FROM PUBLIC;
\connect leapview_control
BEGIN;
REVOKE ALL ON DATABASE leapview_control FROM PUBLIC;
GRANT CONNECT ON DATABASE leapview_control TO leapview_control_runtime, leapview_control_migrator,
    leapview_control_maintenance, leapview_control_upgrade_coordinator;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator;
COMMIT;
\connect leapview_ducklake
BEGIN;
REVOKE ALL ON DATABASE leapview_ducklake FROM PUBLIC;
GRANT CONNECT ON DATABASE leapview_ducklake TO leapview_ducklake_runtime, leapview_ducklake_migrator, leapview_ducklake_maintenance;
GRANT CREATE ON DATABASE leapview_ducklake TO leapview_ducklake_migrator;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA ducklake AUTHORIZATION leapview_ducklake_owner;
REVOKE ALL ON SCHEMA ducklake FROM PUBLIC;
GRANT USAGE ON SCHEMA ducklake TO leapview_ducklake_runtime, leapview_ducklake_maintenance;
GRANT USAGE, CREATE ON SCHEMA ducklake TO leapview_ducklake_migrator;
ALTER DEFAULT PRIVILEGES FOR ROLE leapview_ducklake_owner IN SCHEMA ducklake
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO leapview_ducklake_runtime, leapview_ducklake_maintenance;
ALTER DEFAULT PRIVILEGES FOR ROLE leapview_ducklake_owner IN SCHEMA ducklake
    GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO leapview_ducklake_runtime, leapview_ducklake_maintenance;
COMMIT;
""")
    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--psql", required=True, type=Path)
    parser.add_argument("--socket", required=True, type=Path)
    parser.add_argument("--port", required=True, type=int)
    parser.add_argument("--credentials", required=True, type=Path)
    parser.add_argument("--system-identifier", required=True)
    args = parser.parse_args()
    try:
        if (re.fullmatch(r"/nix/store/[0-9a-z]{32}-postgresql-18[^/]*/bin/psql", str(args.psql)) is None
                or not args.psql.is_file() or args.psql.resolve() != args.psql
                or args.psql.stat().st_uid != 0 or args.psql.stat().st_mode & 0o222
                or not args.socket.is_absolute() or args.socket.resolve() != args.socket
                or not args.socket.is_dir() or not 1 <= args.port <= 65535
                or re.fullmatch(r"[1-9][0-9]{0,19}", args.system_identifier) is None):
            raise BootstrapFailure("operator inputs")
        passwords = private_passwords(args.credentials)
        # Ignore ambient libpq services, URLs, options, passwords and psql hooks.
        environment = {"PATH": "/no-ambient-tools", "HOME": "/no-ambient-config", "LC_ALL": "C"}
        result = subprocess.run([str(args.psql), "-X", "-w", "-h", str(args.socket), "-p", str(args.port),
                                 "-U", "postgres", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-q"],
                                input=bootstrap_sql(passwords, args.system_identifier), text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=environment, timeout=180)
        if result.returncode:
            raise BootstrapFailure("fresh cluster provisioning")
    except (BootstrapFailure, OSError, ValueError, subprocess.SubprocessError):
        # Never propagate psql statements, provider diagnostics or secret paths.
        raise SystemExit("native PostgreSQL bootstrap refused; retain the stopped disposable cluster for diagnosis") from None
    print(json.dumps({"schemaVersion": 1, "kind": "leapview/managed-native-postgres-bootstrap",
                      "status": "provisioned", "systemIdentifier": args.system_identifier,
                      "applicationInitialized": False, "fullManagedProfileQualified": False}, sort_keys=True))


if __name__ == "__main__":
    main()
