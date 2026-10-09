import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_compose_bundled_postgres as bundled
import nix_compose_host_guest as host
import test_nix_compose_host_guest as fixtures


class BundledPostgresTests(unittest.TestCase):
    def test_bundled_operator_input_cannot_borrow_provider_urls(self):
        output = json.dumps({'schema_version': 1, 'pool': {'identity': 'fixture'},
                             'evidence': {'qualification': 'fixture'}}).encode()
        urls = {field: 'postgresql://qualification.invalid/' + field for field, *_ in host.POSTGRES_URLS}
        operator = json.loads(host._qualification_operator_config(output, urls, profile='bundled'))
        self.assertEqual(operator['postgresProfile'], 'bundled')
        self.assertEqual(operator['postgres'], {})
        self.assertEqual(operator['physicalPool']['pool'], {'identity': 'fixture'})
        self.assertNotIn('qualification.invalid', json.dumps(operator))
        with self.assertRaises(host.HostGuestError):
            host._qualification_operator_config(output, urls, profile='unknown')

    def inspection(self):
        return {'id': 'a' * 64, 'imageID': 'sha256:' + 'b' * 64,
            'configuredImage': 'docker.io/library/postgres:18-alpine@sha256:' + 'c' * 64,
            'name': '/leapview-postgres-1', 'state': 'running', 'restartPolicy': 'unless-stopped',
            'project': 'leapview', 'service': 'postgres',
            'ports': {'5432/tcp': None}, 'networks': {'leapview_postgres-private': {}},
            'mounts': [{'Type': 'volume', 'Name': 'leapview_leapview-postgres-data',
                        'Destination': '/var/lib/postgresql', 'RW': True}] + [
                {'Type': 'bind', 'Source': '/opt/leapview/.postgres-secrets/' + name,
                 'Destination': '/run/leapview-postgres-secrets/' + name, 'RW': False}
                for name in bundled.MOUNTED_MATERIAL]}

    def validate(self, value):
        return bundled.validate_inspection(value, self.inspection()['configuredImage'],
                                           'sha256:' + 'b' * 64)

    def test_installer_owned_private_bundled_postgres_is_distinct_from_pool_fixture(self):
        value = self.validate(self.inspection())
        self.assertEqual(value['containerName'], 'leapview-postgres-1')
        self.assertEqual(value['dataVolume'], 'leapview_leapview-postgres-data')
        self.assertEqual(value['privateNetwork'], 'leapview_postgres-private')
        self.assertFalse(value['publishedPorts'])

    def test_wrong_image_service_public_listener_or_secret_mount_fail_closed(self):
        original = self.inspection()
        for key, value in [('imageID', 'sha256:' + 'd' * 64), ('service', 'leapview'),
                           ('state', 'exited'), ('restartPolicy', 'no'),
                           ('networks', {'leapview_default': {}}),
                           ('ports', {'5432/tcp': [{'HostIp': '0.0.0.0', 'HostPort': '5432'}]})]:
            changed = copy.deepcopy(original)
            changed[key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.validate(changed)
        changed = copy.deepcopy(original)
        changed['mounts'][1]['RW'] = True
        with self.assertRaises(ValueError):
            self.validate(changed)
        changed['mounts'][1]['RW'] = False
        changed['mounts'][1]['Source'] = '/var/tmp/fake-password'
        with self.assertRaises(ValueError):
            self.validate(changed)

    def test_probe_reads_actual_installer_owned_secrets_without_emitting_them(self):
        command = bundled.readiness_command('leapview-postgres-1', 'DOCKER_CONFIG=/private')
        self.assertIn('/run/leapview-postgres-secrets/control-runtime-password', command)
        self.assertIn('/run/leapview-postgres-secrets/ducklake-runtime-password', command)
        self.assertIn('PGSSLMODE=verify-full', command)
        self.assertNotIn('POSTGRES_PASSWORD=', command)
        self.assertNotIn('echo', command)
        with self.assertRaises(ValueError):
            bundled.readiness_command('name;cat /etc/shadow', 'DOCKER_CONFIG=/private')

    def test_executed_postreboot_probe_matches_collector_and_rejects_wrong_tls_or_database(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            docker = root / 'docker'
            docker.write_text('#!' + shutil.which('sh') + '\n'
                              'printf "%s\\n" "$PROBE_OUTPUT"\nexit "$PROBE_STATUS"\n')
            docker.chmod(0o700)
            environment = dict(os.environ, PATH=str(root) + os.pathsep + os.environ['PATH'],
                               PROBE_OUTPUT=host.TLS_ROLE_EXPECTATIONS['controlRuntime'], PROBE_STATUS='0')
            command = bundled.readiness_command('leapview-postgres-1', '', control_only=True)
            result = subprocess.run(['sh', '-ec', command], env=environment,
                                    capture_output=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stderr.decode())
            self.assertEqual(host._one_line(result.stdout, 'post-reboot PostgreSQL TLS role probe'),
                             host.TLS_ROLE_EXPECTATIONS['controlRuntime'])
            for output, status in [('leapview_control_runtime|leapview_control|false', '0'),
                    ('leapview_control_runtime|other_database|true', '0'),
                    (host.TLS_ROLE_EXPECTATIONS['controlRuntime'], '2')]:
                environment.update(PROBE_OUTPUT=output, PROBE_STATUS=status)
                rejected = subprocess.run(['sh', '-ec', command], env=environment,
                                          capture_output=True, timeout=5)
                self.assertNotEqual(rejected.returncode, 0)
                self.assertEqual(rejected.stdout, b'')

    def test_private_material_proof_is_compared_inside_guest_without_retaining_secret_hashes(self):
        command = bundled.material_snapshot_command('/var/tmp/qualification/private-material')
        self.assertIn('umask 077', command)
        self.assertIn('sha256sum', command)
        self.assertNotIn('cat /opt/leapview/.postgres-secrets', command)
        verify = bundled.material_verify_command('/var/tmp/qualification/private-material')
        self.assertIn('--check', verify)
        self.assertIn('>/dev/null', verify)
        self.assertIn('rm -f --', verify)

    def test_complete_receipt_requires_real_profile_proofs_and_cannot_relabel_external_fixture(self):
        fixture = fixtures.HostGuestReceiptTests('runTest')
        fixture.setUp()
        self.addCleanup(fixture.tearDown)
        receipt = fixture._receipt()
        receipt['identity']['postgresProfile'] = 'bundled'
        with self.assertRaises(host.HostGuestError):
            host._validate_receipt(receipt, fixture.evidence)
        metadata = {}
        raw = self.inspection()
        raw.update(id=fixture.pg_container_id, imageID=fixture.postgres_fixture['imageID'],
                   configuredImage=fixture.postgres_fixture['image'])
        for phase, prefix in [('beforePendingReboot', 'bundled-postgres-before-pending-reboot'),
                ('afterPendingReboot', 'bundled-postgres-after-pending-reboot'),
                ('afterPublicReboot', 'bundled-postgres-after-public-reboot')]:
            fixture._write(prefix + '-inspection.json', json.dumps(raw).encode())
            fixture._write(prefix + '-network-internal.txt', b'true\n')
            fixture._write(prefix + '-volume-labels.json', json.dumps({
                'com.docker.compose.project': 'leapview', 'com.docker.compose.volume': 'leapview-postgres-data'}).encode())
            fixture._write(prefix + '-tls-role-probes.txt',
                b'leapview_control_runtime|leapview_control|true\nleapview_ducklake_runtime|leapview_ducklake|true\n')
            metadata[phase] = dict(bundled.validate_inspection(raw, fixture.postgres_fixture['image'],
                fixture.postgres_fixture['imageID']), privateNetworkInternal=True)
            if phase != 'beforePendingReboot':
                fixture._write(prefix + '-material-preserved.json', json.dumps({
                    'credentialsPreserved': True, 'tlsMaterialPreserved': True, 'privateProofRemoved': True}).encode())
        fixture._write('bundled-postgres-pool-fixture-removed.txt', b'absent\n')
        receipt['guest']['bundledPostgres'] = metadata
        report = json.loads((fixture.evidence / 'qualification-report.json').read_bytes())
        report['postgresProfile'] = 'bundled'
        fixture._write('qualification-report.json', json.dumps(report).encode())
        receipt['evidenceInventory'] = host.qualification._qualification_evidence_inventory(fixture.evidence)
        self.assertIs(host._validate_receipt(receipt, fixture.evidence), receipt)
        for filename, value in [('bundled-postgres-after-public-reboot-network-internal.txt', b'false'),
                ('bundled-postgres-after-pending-reboot-material-preserved.json', b'{}')]:
            path = fixture.evidence / filename
            previous = path.read_bytes()
            path.write_bytes(value)
            receipt['evidenceInventory'] = host.qualification._qualification_evidence_inventory(fixture.evidence)
            with self.subTest(filename=filename), self.assertRaises(ValueError):
                host._validate_receipt(receipt, fixture.evidence)
            path.write_bytes(previous)
