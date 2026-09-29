"""Fail-closed tests for the interrupted demo-02 first-install preflight."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from urllib.parse import quote


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    'demo_install_preflight', ROOT / 'scripts' / 'demo_install_preflight.py')
preflight = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = preflight
SPEC.loader.exec_module(preflight)


def env_text(values):
    return ''.join(key + '=' + value + '\n' for key, value in values.items()).encode()


def fixture_values():
    deployment = {
        'LEAPVIEW_IMAGE': preflight.APP_IMAGE,
        'COMPOSE_PROJECT_NAME': preflight.PROJECT,
        'COMPOSE_APP_BIND': preflight.APP_BIND,
        'COMPOSE_HTTPS': '1',
        'CADDY_DOMAIN': preflight.DOMAIN,
        'CADDY_HTTP_BIND': '80',
        'CADDY_HTTPS_BIND': '443',
        'CADDY_HTTPS_UDP_BIND': '443',
        'CADDY_IMAGE': 'caddy:2.10.2-alpine@sha256:' + 'd' * 64,
    }
    app = {
        'LEAPVIEW_PRODUCTION': '1',
        'LEAPVIEW_ENVIRONMENT': 'prod',
        'LEAPVIEW_ADDR': ':8080',
        'LEAPVIEW_HOME': preflight.APP_HOME,
        'LEAPVIEW_PUBLIC_URL': 'https://' + preflight.DOMAIN,
        'LEAPVIEW_ALLOWED_HOSTS': preflight.DOMAIN,
        'LEAPVIEW_COOKIE_SECURE': 'true',
        'LEAPVIEW_TRUST_PROXY_HEADERS': 'true',
        'LEAPVIEW_POSTGRES_REQUIRE_TLS': 'true',
        'LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE': 'control_runtime',
        'LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE': 'control_migrator',
        'LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_ROLE': 'control_maintenance',
        'LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE': 'ducklake_runtime',
        'LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_ROLE': 'ducklake_maintenance',
    }
    for name, url_key, database, role_key in preflight.ROLE_URLS:
        role = app[role_key]
        password = 'p@ss:control' if name == 'control-runtime' else 'provider-secret-' + name
        app[url_key] = (
            'postgresql://' + role + ':' + quote(password, safe='') +
            '@' + preflight.POSTGRES + ':5432/' + database +
            '?sslmode=verify-full&sslrootcert=%2Fvar%2Flib%2Fleapview%2Fhome%2Fpostgres-root.crt'
        )
    app['LEAPVIEW_AGENT_API_KEY'] = 'existing-agent-provider-password'
    app['LEAPVIEW_CSRF_KEY'] = '<generated-by-leapviewctl>'
    app['LEAPVIEW_METRICS_BEARER_TOKEN'] = '<generated-by-leapviewctl>'
    app['LEAPVIEW_AGENT_CREDENTIAL_KEY'] = '<generated-by-leapviewctl>'
    return deployment, app


class FakeBackend:
    def __init__(self):
        self.events = []
        self.ca = None
        self.fail = None
        self.fail_once = set()

    def event(self, name):
        self.events.append(name)
        if self.fail == name or name in self.fail_once:
            self.fail_once.discard(name)
            raise RuntimeError('provider-secret-control-runtime')

    def hostname(self):
        self.event('hostname')
        return preflight.HOST

    def image(self, image):
        self.event('image')
        self.asserted_image = image
        return {
            'Id': 'sha256:' + '1' * 64,
            'RepoDigests': [preflight.APP_IMAGE],
            'Config': {'Labels': {
                'org.opencontainers.image.revision': preflight.APP_REVISION,
                'dev.leapview.build.dirty': 'false',
            }},
        }

    def postgres(self):
        self.event('postgres')
        return {
            'Name': '/' + preflight.POSTGRES,
            'Config': {
                'Image': 'postgres:18.0@sha256:' + '2' * 64,
                'Labels': {'com.docker.compose.project': preflight.PROJECT},
            },
            'State': {'Running': True, 'Health': {'Status': 'healthy'}},
            'NetworkSettings': {
                'Ports': {'5432/tcp': None},
                'Networks': {preflight.NETWORK: {}},
            },
        }

    def network(self):
        self.event('network')
        return {'Name': preflight.NETWORK}

    def volume(self):
        self.event('volume')
        return {'Name': preflight.VOLUME}

    def optional_app_container(self):
        self.event('app-container')
        return None

    def postgres_version(self):
        self.event('postgres-version')
        return 'postgres (PostgreSQL) 18.0'

    def check_home_owner(self):
        self.event('home-owner')

    def validate_ca_source(self, source):
        self.event('ca-source-validation')

    def install_ca(self, source):
        self.event('install-ca')
        contents = Path(source).read_bytes()
        if self.ca is not None and self.ca != contents:
            raise RuntimeError('CA changed')
        self.ca = contents

    def verify_ca_as_app(self):
        self.event('verify-ca')
        return hashlib.sha256(self.ca).hexdigest()

    def empty_database(self, database):
        self.event('schema:' + database)

    def probe_role(self, postgres_image, role):
        self.event('role:' + role['name'])
        self.asserted_postgres_image = postgres_image
        self.asserted_password = role['password']

    def validate_config(self, app_env_path):
        self.event('validate-config')
        self.validated_env = Path(app_env_path).read_bytes()


class InstallPreflightTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        base = Path(self.temp.name)
        self.bootstrap = base / 'opt' / 'leapview-cfo-bootstrap'
        self.install = base / 'opt' / 'leapview'
        self.provider = base / 'etc' / 'leapview-provider-cfo'
        self.bootstrap.mkdir(parents=True, mode=0o700)
        self.provider.mkdir(parents=True, mode=0o700)
        self.ca = self.provider / 'postgres-root.crt'
        self.ca.write_bytes(b'-----BEGIN CERTIFICATE-----\nprovider CA fixture\n-----END CERTIFICATE-----\n')
        self.ca.chmod(0o600)
        deployment, app = fixture_values()
        self.deployment = env_text(deployment)
        self.application = env_text(app)
        for name, contents in (('deployment.env', self.deployment), ('leapview.env', self.application)):
            path = self.bootstrap / name
            path.write_bytes(contents)
            path.chmod(0o600)
        self.paths = preflight.Paths(self.bootstrap, self.install, self.provider)
        self.backend = FakeBackend()
        self.owner = os.geteuid()

    def run_preflight(self):
        return preflight.run_preflight(self.ca, backend=self.backend, paths=self.paths,
                                       enforce_root=False)

    def test_success_installs_ca_before_url_roles_and_config_validation(self):
        report = self.run_preflight()
        self.assertEqual(report['status'], 'preflight-ready')
        self.assertEqual(report['image'], preflight.APP_IMAGE)
        self.assertEqual(report['sourceRevision'], preflight.APP_REVISION)
        self.assertEqual(report['applicationBinding'], '127.0.0.1:8081')
        self.assertEqual(report['postgresSchema'], 'empty')
        self.assertFalse(report['applicationInitialized'])
        self.assertEqual(report['checkedRoles'], [role[0] for role in preflight.ROLE_URLS])
        self.assertEqual(set(report['generatedKeyNames']), set(preflight.SECRET_KEYS))
        self.assertNotIn('provider-secret', json.dumps(report))
        self.assertNotIn('p@ss:control', json.dumps(report))

        names = self.backend.events
        self.assertLess(names.index('install-ca'), names.index('schema:leapview_control'))
        self.assertLess(names.index('verify-ca'), names.index('role:control-runtime'))
        self.assertLess(names.index('role:ducklake-maintenance'), names.index('validate-config'))
        self.assertEqual(self.backend.ca, self.ca.read_bytes())
        self.assertEqual(self.backend.asserted_image, preflight.APP_IMAGE)
        self.assertEqual(self.backend.asserted_postgres_image,
                         'postgres:18.0@sha256:' + '2' * 64)

        values = preflight._parse_env((self.install / 'leapview.env').read_bytes(), 'test')
        self.assertEqual(values['LEAPVIEW_POSTGRES_CONTROL_URL'],
                         preflight._parse_env(self.application, 'test')['LEAPVIEW_POSTGRES_CONTROL_URL'])
        self.assertEqual(values['LEAPVIEW_AGENT_API_KEY'], 'existing-agent-provider-password')
        for key in preflight.SECRET_KEYS:
            self.assertEqual(len(values[key]), 64)
        self.assertFalse((self.install / 'initial-credentials.json').exists())
        self.assertFalse((self.install / '.host-install.json').exists())

    def test_config_failure_keeps_generated_keys_and_retry_reuses_them(self):
        self.backend.fail_once.add('validate-config')
        with self.assertRaisesRegex(preflight.PreflightError, 'production application configuration validation failed') as raised:
            self.run_preflight()
        self.assertNotIn('provider-secret-control-runtime', str(raised.exception))
        first = preflight._parse_env((self.install / 'leapview.env').read_bytes(), 'test')
        self.assertTrue(all(first[key] for key in preflight.SECRET_KEYS))
        self.assertFalse((self.install / 'initial-credentials.json').exists())
        self.assertFalse((self.install / '.host-install.json').exists())

        self.backend.events.clear()
        report = self.run_preflight()
        second = preflight._parse_env((self.install / 'leapview.env').read_bytes(), 'test')
        self.assertEqual({key: second[key] for key in preflight.SECRET_KEYS},
                         {key: first[key] for key in preflight.SECRET_KEYS})
        self.assertEqual(report['generatedKeyNames'], [])
        self.assertEqual(self.backend.ca, self.ca.read_bytes())

    def test_wrong_binding_fails_before_image_or_ca_steps(self):
        deployment = preflight._parse_env(self.deployment, 'test')
        deployment['COMPOSE_APP_BIND'] = '127.0.0.1:8080'
        (self.bootstrap / 'deployment.env').write_bytes(env_text(deployment))
        with self.assertRaisesRegex(preflight.PreflightError, 'COMPOSE_APP_BIND'):
            self.run_preflight()
        self.assertEqual(self.backend.events, ['hostname'])
        self.assertFalse(self.install.exists())

    def test_wrong_image_fails_closed(self):
        deployment = preflight._parse_env(self.deployment, 'test')
        deployment['LEAPVIEW_IMAGE'] = 'ghcr.io/flidai/leapview:latest'
        (self.bootstrap / 'deployment.env').write_bytes(env_text(deployment))
        with self.assertRaisesRegex(preflight.PreflightError, 'LEAPVIEW_IMAGE'):
            self.run_preflight()
        self.assertEqual(self.backend.events, ['hostname'])
        self.assertIsNone(self.backend.ca)

    def test_wrong_home_owner_stops_before_ca_url_or_database_checks(self):
        self.backend.fail = 'home-owner'
        with self.assertRaisesRegex(preflight.PreflightError, 'application home ownership failed'):
            self.run_preflight()
        self.assertNotIn('install-ca', self.backend.events)
        self.assertFalse(any(event.startswith('role:') for event in self.backend.events))
        self.assertFalse(self.install.exists())

    def test_ca_copy_failure_precedes_database_url_validation_and_connections(self):
        app = preflight._parse_env(self.application, 'test')
        app['LEAPVIEW_POSTGRES_CONTROL_URL'] = 'not a database URL'
        (self.bootstrap / 'leapview.env').write_bytes(env_text(app))
        self.backend.fail = 'install-ca'
        with self.assertRaisesRegex(preflight.PreflightError, 'application CA installation failed'):
            self.run_preflight()
        self.assertNotIn('verify-ca', self.backend.events)
        self.assertNotIn('schema:leapview_control', self.backend.events)
        self.assertFalse(any(event.startswith('role:') for event in self.backend.events))
        self.assertFalse(self.install.exists())

    def test_invalid_database_url_is_checked_only_after_ca_install(self):
        app = preflight._parse_env(self.application, 'test')
        app['LEAPVIEW_POSTGRES_CONTROL_URL'] = 'not a database URL'
        (self.bootstrap / 'leapview.env').write_bytes(env_text(app))
        with self.assertRaisesRegex(preflight.PreflightError, 'LEAPVIEW_POSTGRES_CONTROL_URL'):
            self.run_preflight()
        self.assertIn('install-ca', self.backend.events)
        self.assertIn('verify-ca', self.backend.events)
        self.assertFalse(any(event.startswith('role:') for event in self.backend.events))
        self.assertFalse(self.install.exists())

    def test_tls_role_failure_stops_later_roles_and_configuration_validation(self):
        self.backend.fail = 'role:control-migrator'
        with self.assertRaisesRegex(preflight.PreflightError, 'control-migrator TLS role connection failed') as raised:
            self.run_preflight()
        self.assertNotIn('provider-secret-control-runtime', str(raised.exception))
        self.assertIn('role:control-runtime', self.backend.events)
        self.assertIn('role:control-migrator', self.backend.events)
        self.assertNotIn('role:control-maintenance', self.backend.events)
        self.assertNotIn('validate-config', self.backend.events)
        self.assertFalse(self.install.exists())

    def test_nonempty_database_stops_before_role_connections(self):
        self.backend.fail = 'schema:leapview_control'
        with self.assertRaisesRegex(preflight.PreflightError, 'leapview_control empty-schema check failed'):
            self.run_preflight()
        self.assertIn('install-ca', self.backend.events)
        self.assertNotIn('role:control-runtime', self.backend.events)
        self.assertFalse(self.install.exists())

    def test_operation_only_ducklake_migrator_credential_is_rejected(self):
        app = preflight._parse_env(self.application, 'test')
        app['LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL'] = 'postgresql://secret'
        (self.bootstrap / 'leapview.env').write_bytes(env_text(app))
        with self.assertRaisesRegex(preflight.PreflightError, 'operation-only DuckLake') as raised:
            self.run_preflight()
        self.assertNotIn('postgresql://secret', str(raised.exception))
        self.assertIn('install-ca', self.backend.events)
        self.assertFalse(any(event.startswith('role:') for event in self.backend.events))

    def test_tls_and_public_origin_require_final_https_values(self):
        deployment = preflight._parse_env(self.deployment, 'test')
        deployment['COMPOSE_HTTPS'] = '0'
        (self.bootstrap / 'deployment.env').write_bytes(env_text(deployment))
        with self.assertRaisesRegex(preflight.PreflightError, 'COMPOSE_HTTPS'):
            self.run_preflight()
        self.assertEqual(self.backend.events, ['hostname'])

        deployment['COMPOSE_HTTPS'] = '1'
        (self.bootstrap / 'deployment.env').write_bytes(env_text(deployment))
        app = preflight._parse_env(self.application, 'test')
        app['LEAPVIEW_PUBLIC_URL'] = 'http://' + preflight.DOMAIN
        (self.bootstrap / 'leapview.env').write_bytes(env_text(app))
        self.backend.events.clear()
        with self.assertRaisesRegex(preflight.PreflightError, 'LEAPVIEW_PUBLIC_URL'):
            self.run_preflight()
        self.assertIn('install-ca', self.backend.events)
        self.assertFalse(any(event.startswith('role:') for event in self.backend.events))

    def test_private_file_read_errors_do_not_disclose_contents_or_credentials(self):
        secret = 'provider-secret-control-runtime'
        with patch.object(preflight.subprocess, 'run', return_value=subprocess.CompletedProcess(
                ['docker'], 31, b'', secret.encode())):
            with self.assertRaises(preflight.PreflightError) as raised:
                preflight._checked('TLS role connection', ['docker', 'exec', secret])
        self.assertNotIn(secret, str(raised.exception))

    def test_docker_operations_use_verified_shell_uid_and_never_initialize(self):
        backend = preflight.DockerBackend()
        with tempfile.TemporaryDirectory() as directory:
            ca = Path(directory) / 'ca.crt'
            ca.write_text('certificate')
            env = Path(directory) / 'leapview.env'
            env.write_text('LEAPVIEW_CSRF_KEY=placeholder\n')
            with patch.object(preflight, '_checked', return_value='') as checked:
                backend.check_home_owner()
                self.assertIn('/busybox/sh', checked.call_args.args[1])
                self.assertIn('999:999', checked.call_args.args[1])
                backend.install_ca(ca)
                install_args = checked.call_args.args[1]
                self.assertIn('/busybox/sh', install_args)
                self.assertIn('ghcr.io/flidai/leapview@sha256:' + 'bc07ec074a4df412f3cb1a81c32b35a54d92494e3f42dcdc115942ec81029540', install_args)
                self.assertIn('0:0', install_args)
                backend.validate_config(env)
                validate_args = checked.call_args.args[1]
                self.assertEqual(validate_args[-3:], ['config', 'validate', '--production'])
                for forbidden in ('admin', 'initialize', 'start', 'host', 'install'):
                    self.assertNotIn(forbidden, validate_args)

    def test_tls_password_is_sent_on_stdin_and_never_in_command_arguments(self):
        backend = preflight.DockerBackend()
        role = {
            'name': 'control-runtime',
            'host': preflight.POSTGRES,
            'port': 5432,
            'database': 'leapview_control',
            'username': 'control_runtime',
            'password': 'provider @: secret',
        }
        with patch.object(preflight, '_checked', return_value='control_runtime\tleapview_control') as checked:
            backend.probe_role('postgres:18@sha256:' + '2' * 64, role)
        args = checked.call_args.args[1]
        stdin = checked.call_args.kwargs['input_bytes']
        self.assertNotIn(role['password'], ' '.join(args))
        self.assertEqual(stdin, role['password'].encode() + b'\n')


if __name__ == '__main__':
    unittest.main()
