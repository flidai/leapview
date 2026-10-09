#!/usr/bin/env python3
"""Disposable restored-data PostgreSQL component rehearsal, never a host updater."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time


def run(args, *, sql=None, check=True):
    result = subprocess.run([str(arg) for arg in args], input=sql, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=180)
    if check and result.returncode:
        raise RuntimeError(f'{args[0]} failed ({result.returncode}):\n{result.stdout}')
    return result.stdout.strip(), result.returncode


class Cluster:
    def __init__(self, binary, data, socket, port):
        self.binary, self.data, self.socket, self.port = binary, data, socket, str(port)

    def initialize(self):
        run([self.binary / 'initdb', '-D', self.data, '--username=postgres',
             '--locale=C', '--encoding=UTF8', '--data-checksums', '--auth-local=trust',
             '--auth-host=reject'])

    def start(self):
        # The enclosing directory and socket are private. No TCP listener exists.
        options = f"-h '' -k {self.socket} -p {self.port} -c unix_socket_permissions=0700"
        run([self.binary / 'pg_ctl', '-D', self.data, '-l', self.data.parent / (self.data.name + '.log'),
             '-w', '-t', '60', '-o', options, 'start'])

    def stop(self):
        if (self.data / 'postmaster.pid').exists():
            run([self.binary / 'pg_ctl', '-D', self.data, '-w', '-t', '60', '-m', 'fast', 'stop'])

    def sql(self, statement, *, user='postgres', database='postgres', check=True):
        return run([self.binary / 'psql', '-X', '-At', '-v', 'ON_ERROR_STOP=1',
                    '-h', self.socket, '-p', self.port, '-U', user, '-d', database],
                   sql=statement, check=check)


SEED = """
CREATE ROLE fixture_owner NOLOGIN;
CREATE ROLE fixture_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
CREATE DATABASE rehearsal OWNER fixture_owner;
REVOKE ALL ON DATABASE rehearsal FROM PUBLIC;
GRANT CONNECT ON DATABASE rehearsal TO fixture_runtime;
"""
DATA = """
SET ROLE fixture_owner;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA owned AUTHORIZATION fixture_owner;
CREATE TABLE owned.acknowledged (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    publication uuid NOT NULL, payload jsonb NOT NULL, bytes bytea NOT NULL
);
INSERT INTO owned.acknowledged(publication, payload, bytes)
SELECT '00000000-0000-4000-8000-000000000001'::uuid,
       jsonb_build_object('sequence', n, 'value', repeat('restored-data', 1000)),
       decode('00ff42', 'hex') FROM generate_series(1, 100) n;
GRANT USAGE ON SCHEMA owned TO fixture_runtime;
GRANT SELECT, INSERT ON owned.acknowledged TO fixture_runtime;
GRANT USAGE ON SEQUENCE owned.acknowledged_id_seq TO fixture_runtime;
"""
FINGERPRINT = """
SELECT count(*)::text || '|' || md5(string_agg(
 id::text || publication::text || payload::text || encode(bytes, 'hex'), '' ORDER BY id))
FROM owned.acknowledged;
"""


def verify(cluster, expected):
    actual, _ = cluster.sql(FINGERPRINT, user='fixture_runtime', database='rehearsal')
    if actual != expected:
        raise RuntimeError('acknowledged restored data or runtime read authority differs')
    attributes, _ = cluster.sql("SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication "
                                "FROM pg_roles WHERE rolname='fixture_runtime'")
    if attributes != 'f':
        raise RuntimeError('runtime gained privileged role attributes')
    for statement in ('CREATE TABLE owned.forbidden(id int)',
                      'ALTER TABLE owned.acknowledged ADD COLUMN forbidden int'):
        _, code = cluster.sql(statement, user='fixture_runtime', database='rehearsal', check=False)
        if code == 0:
            raise RuntimeError('runtime gained schema ownership or creation authority')


def rehearse(old_bin, new_bin, report):
    if os.geteuid() == 0:
        raise ValueError('run the disposable rehearsal as an unprivileged user (Nix build user)')
    # Environment overrides cannot select a service, data directory or psql startup file.
    for key in list(os.environ):
        if key.startswith('PG'):
            del os.environ[key]
    versions = [run([binary / 'postgres', '--version'])[0] for binary in (old_bin, new_bin)]
    if ' 17.' not in versions[0] or ' 18.' not in versions[1]:
        raise ValueError('this rehearsal is qualified only for locked PostgreSQL 17 -> 18 tools')
    with tempfile.TemporaryDirectory(prefix='pg-major-') as temporary:
        root = Path(temporary)
        socket = root / 'socket'
        socket.mkdir(mode=0o700)
        original = Cluster(old_bin, root / 'original', socket, 55431)
        restored = Cluster(old_bin, root / 'restored', socket, 55431)
        candidate = Cluster(new_bin, root / 'candidate', socket, 55432)
        try:
            original.initialize()
            original.start()
            original.sql(SEED)
            original.sql(DATA, database='rehearsal')
            expected, _ = original.sql(FINGERPRINT, user='fixture_runtime', database='rehearsal')
            identity, _ = original.sql('SELECT system_identifier FROM pg_control_system()')
            run([old_bin / 'pg_basebackup', '-h', socket, '-p', original.port, '-U', 'postgres',
                 '-D', restored.data, '-X', 'stream', '--checkpoint=fast'])
            original.stop()
            run([old_bin / 'pg_verifybackup', restored.data])
            restored.start()
            verify(restored, expected)
            restored_identity, _ = restored.sql('SELECT system_identifier FROM pg_control_system()')
            if identity != restored_identity:
                raise RuntimeError('restored cluster does not retain the backed-up system identity')
            restored.stop()
            candidate.initialize()
            upgrade = [new_bin / 'pg_upgrade', '--old-bindir', old_bin, '--new-bindir', new_bin,
                       '--old-datadir', restored.data, '--new-datadir', candidate.data,
                       '--username=postgres', '--socketdir', socket, '--old-port', restored.port,
                       '--new-port', candidate.port, '--copy']
            started = time.monotonic()
            run(upgrade + ['--check'])
            run(upgrade)
            elapsed = time.monotonic() - started
            candidate.start()
            verify(candidate, expected)
            candidate_identity, _ = candidate.sql('SELECT system_identifier FROM pg_control_system()')
            if candidate_identity == identity:
                raise RuntimeError('new major cluster unexpectedly reused the old system identity')
            candidate.stop()
            # Before new writes, the copied old cluster is a verified recovery point.
            restored.start()
            verify(restored, expected)
            restored.stop()
            candidate.start()
            inserted, _ = candidate.sql("INSERT INTO owned.acknowledged(publication,payload,bytes) "
                "VALUES ('00000000-0000-4000-8000-000000000002', '{\"afterUpgrade\":true}', "
                "decode('ff', 'hex')) RETURNING id", user='fixture_runtime', database='rehearsal')
            if inserted.splitlines()[0] != '101':
                raise RuntimeError('identity sequence or runtime insert authority was not preserved')
            candidate.stop()
            candidate.start()
            count, _ = candidate.sql('SELECT count(*) FROM owned.acknowledged',
                                     user='fixture_runtime', database='rehearsal')
            if count != '101':
                raise RuntimeError('post-upgrade acknowledged write did not survive restart')
            candidate.stop()
            # This intentionally demonstrates why old-directory rollback loses new writes.
            restored.start()
            verify(restored, expected)
            restored.stop()
            report.parent.mkdir(parents=True, exist_ok=True)
            report.write_text(json.dumps({
                'schema': 'leapview.postgres-major-component-rehearsal/v1',
                'scope': 'disposable representative database; not a supported LeapView 17 deployment',
                'versions': versions, 'physicalBackupVerified': True, 'restoredIdentityVerified': True,
                'upgradeMode': 'copy', 'checkAndUpgradeSeconds': round(elapsed, 3),
                'acknowledgedRowsBefore': 100, 'acknowledgedRowsAfter': 101,
                'dataRoleAndSequenceChecksPassed': True, 'candidateRestartPassed': True,
                'retainedOldClusterRestartPassed': True,
                'oldClusterRollbackBoundary': 'before any candidate writes; thereafter restore/reconcile',
                'productProfileQualified': False,
            }, indent=2) + '\n')
        finally:
            for cluster in (candidate, restored, original):
                cluster.stop()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--old-bin', required=True, type=Path)
    parser.add_argument('--new-bin', required=True, type=Path)
    parser.add_argument('--report', required=True, type=Path)
    args = parser.parse_args()
    rehearse(args.old_bin, args.new_bin, args.report)
