import copy
import json
import os
from pathlib import Path
import shutil
import ssl
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_candidate_manifest as candidate
import nix_oci_content as oci
import nix_registry_content as registry


class RegistryContentTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name) / 'fixture'
        (self.root / 'blobs/sha256').mkdir(parents=True)
        (self.root / 'oci-layout').write_text('{"imageLayoutVersion":"1.0.0"}')
        layer = b'original candidate layer'
        config = {'os': 'linux', 'architecture': 'amd64',
                  'rootfs': {'type': 'layers', 'diff_ids': [candidate.digest_bytes(layer)]}}
        config_bytes = candidate.canonical_bytes(config)
        self.record = {'artifact': {'kind': 'application-image', 'platform': 'linux/amd64',
                                   'version': 'candidate', 'configDigest': candidate.digest_bytes(config_bytes),
                                   'layerDiffIDs': config['rootfs']['diff_ids']},
                       'source': {'revision': 'a' * 40}, 'candidateDigest': 'sha256:' + 'b' * 64,
                       'requiredReleaseEvidence': ['provenance'], 'releaseAdmission': False}
        manifest = {'schemaVersion': 2, 'mediaType': oci.MANIFEST,
                    'config': self.blob(config_bytes, oci.CONFIG),
                    'layers': [self.blob(layer, oci.LAYER)]}
        descriptor = self.blob(candidate.canonical_bytes(manifest), oci.MANIFEST)
        self.digest = descriptor['digest']
        self.image = 'ghcr.io/flidai/leapview@' + self.digest
        (self.root / 'index.json').write_text(json.dumps({'schemaVersion': 2, 'manifests': [descriptor]}))
        self.commands = []

    def blob(self, data, media):
        digest = candidate.digest_bytes(data)
        (self.root / 'blobs/sha256' / digest[7:]).write_bytes(data)
        return {'digest': digest, 'size': len(data), 'mediaType': media}

    def transport(self, args, **kwargs):
        self.commands.append((args, kwargs))
        destination = args[-1].removeprefix('oci:').removesuffix(':candidate')
        shutil.copytree(self.root, destination)
        return subprocess.CompletedProcess(args, 0)

    def bind(self, **kwargs):
        return registry.bind_registry(self.image, 'application-image', [self.record], ['linux/amd64'], **kwargs)

    def test_fetches_every_platform_without_changing_digests_and_checks_content(self):
        with patch.object(registry.subprocess, 'run', side_effect=self.transport):
            result = self.bind()
        command, options = self.commands[0]
        self.assertEqual(command[:6], ['skopeo', 'copy', '--all', '--preserve-digests', '--src-tls-verify=true', 'docker://' + self.image])
        self.assertNotIn('shell', options)
        self.assertTrue(options['check'])
        self.assertEqual(options['timeout'], 300)
        self.assertEqual(options['stderr'], subprocess.DEVNULL)
        self.assertEqual(result['image'], self.image)
        self.assertEqual(result['contentBinding']['oci']['digest'], self.digest)
        self.assertEqual(result['contentBinding']['platforms'][0]['candidateDigest'], self.record['candidateDigest'])
        self.assertFalse(result['releaseAdmission'])
        self.assertFalse(result['contentBinding']['releaseAdmission'])
        self.assertFalse(Path(command[-1].removeprefix('oci:').removesuffix(':candidate')).exists())
        registry.verify(result, result)
        changed = copy.deepcopy(result)
        changed['releaseAdmission'] = 0
        with self.assertRaises(ValueError):
            registry.verify(changed, result)

    def test_mutable_foreign_cross_output_or_noncanonical_references_never_fetch(self):
        images = ['ghcr.io/flidai/leapview:main', 'ghcr.io/flidai/leapview:v1@' + self.digest,
                  'ghcr.io/attacker/leapview@' + self.digest, 'ghcr.io/flidai/leapview-site@' + self.digest,
                  self.image + '\n', 'https://' + self.image, self.image.upper()]
        with patch.object(registry.subprocess, 'run') as run:
            for image in images:
                with self.subTest(image=image), self.assertRaises(ValueError):
                    registry.bind_registry(image, 'application-image', [self.record], ['linux/amd64'])
            run.assert_not_called()

    def test_requested_digest_and_fresh_candidate_must_match_downloaded_content(self):
        with patch.object(registry.subprocess, 'run', side_effect=self.transport):
            with self.assertRaises(ValueError):
                registry.bind_registry('ghcr.io/flidai/leapview@sha256:' + 'f' * 64,
                                       'application-image', [self.record], ['linux/amd64'])
            changed = copy.deepcopy(self.record)
            changed['artifact']['configDigest'] = 'sha256:' + 'f' * 64
            with self.assertRaises(ValueError):
                registry.bind_registry(self.image, 'application-image', [changed], ['linux/amd64'])
            with self.assertRaises(ValueError):
                registry.bind_registry(self.image, 'application-image', [self.record], ['linux/amd64', 'linux/arm64'])

    def test_site_uses_its_own_repository_and_content_kind(self):
        self.record['artifact']['kind'] = 'site-image'
        image = 'ghcr.io/flidai/leapview-site@' + self.digest
        with patch.object(registry.subprocess, 'run', side_effect=self.transport):
            result = registry.bind_registry(image, 'site-image', [self.record], ['linux/amd64'])
        self.assertEqual(result['image'], image)

    def test_registry_failures_are_bounded_and_do_not_leak_tool_diagnostics(self):
        for error in [subprocess.CalledProcessError(1, ['skopeo'], stderr=b'password=secret'),
                      subprocess.TimeoutExpired(['skopeo'], 300), FileNotFoundError('token=secret')]:
            with self.subTest(error=error), patch.object(registry.subprocess, 'run', side_effect=error):
                with self.assertRaisesRegex(ValueError, '^registry content could not be fetched$'):
                    self.bind()

    def test_expired_or_changed_candidate_after_download_cannot_write_receipt(self):
        output = self.root.parent / 'receipt.json'
        args = ['nix_registry_content.py', '--image', self.image, '--kind', 'application-image',
                '--platform', 'linux/amd64', '--candidate', 'archive', 'manifest', 'runtime',
                '--output', str(output)]
        changed = copy.deepcopy(self.record)
        changed['candidateDigest'] = 'sha256:' + 'f' * 64
        for after in [ValueError('expired runtime evidence'), [changed]]:
            with self.subTest(after=after), patch.object(sys, 'argv', args), \
                    patch.object(candidate, 'checkout_source', return_value=self.record['source']), \
                    patch.object(registry, 'verified_records', side_effect=[[self.record], after]), \
                    patch.object(registry.subprocess, 'run', side_effect=self.transport):
                with self.assertRaises(SystemExit):
                    registry.main()
                self.assertFalse(output.exists())

    @unittest.skipUnless(os.environ.get('LEAPVIEW_TEST_NIX_REGISTRY') == '1', 'requires locked Skopeo')
    def test_real_skopeo_reads_tls_registry_by_digest(self):
        certificate = self.root.parent / 'certificate.pem'
        key = self.root.parent / 'key.pem'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                        '-subj', '/CN=localhost', '-addext', 'subjectAltName=IP:127.0.0.1',
                        '-keyout', str(key), '-out', str(certificate)],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        root, digest = self.root, self.digest
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                requests.append(self.path)
                if self.path == '/v2/':
                    data, media = b'{}', 'application/json'
                elif self.path == '/v2/fixture/manifests/' + digest:
                    data, media = (root / 'blobs/sha256' / digest[7:]).read_bytes(), oci.MANIFEST
                elif self.path.startswith('/v2/fixture/blobs/sha256:'):
                    data, media = (root / 'blobs/sha256' / self.path.rsplit(':', 1)[1]).read_bytes(), 'application/octet-stream'
                else:
                    self.send_error(404)
                    return
                self.send_response(200)
                self.send_header('Content-Type', media)
                self.send_header('Content-Length', str(len(data)))
                self.send_header('Docker-Distribution-API-Version', 'registry/2.0')
                self.send_header('Docker-Content-Digest', candidate.digest_bytes(data))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *_):
                pass

        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(certificate, key)
        server.socket = context.wrap_socket(server.socket, server_side=True)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        repository = '127.0.0.1:' + str(server.server_port) + '/fixture'
        try:
            with patch.dict(registry.REPOSITORIES, {'application-image': repository}), \
                    patch.dict(os.environ, {'SSL_CERT_FILE': str(certificate)}):
                result = registry.bind_registry(repository + '@' + digest, 'application-image',
                                                [self.record], ['linux/amd64'])
            self.assertEqual(result['contentBinding']['oci']['digest'], digest)
            self.assertIn('/v2/fixture/manifests/' + digest, requests)
            self.assertFalse(result['releaseAdmission'])
            with patch.dict(registry.REPOSITORIES, {'application-image': repository}), \
                    patch.dict(os.environ, {'SSL_CERT_FILE': '/dev/null'}):
                with self.assertRaisesRegex(ValueError, '^registry content could not be fetched$'):
                    registry.bind_registry(repository + '@' + digest, 'application-image',
                                           [self.record], ['linux/amd64'])
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)


if __name__ == '__main__':
    unittest.main()
