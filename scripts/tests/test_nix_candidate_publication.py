import copy
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import itertools
import json
import os
from pathlib import Path
import ssl
import subprocess
import sys
import threading
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_candidate_publication as publication
import nix_candidate_manifest as candidate
import nix_registry_content as registry
import test_nix_registry_content as fixtures


class PublicationTests(unittest.TestCase):
    blob = fixtures.RegistryContentTests.blob

    def setUp(self):
        fixtures.RegistryContentTests.setUp(self)
        self.runtime = self.root.parent / 'runtime'
        self.runtime.mkdir()
        self.document = {'spdxVersion': 'SPDX-2.3', 'packages': [{'name': 'glibc'}]}
        data = candidate.canonical_bytes(self.document)
        (self.runtime / 'sbom.spdx.json').write_bytes(data)
        self.record['evidence'] = {'nix-runtime': {'reports': [
            {'path': 'sbom.spdx.json', 'sha256': candidate.digest_bytes(data)}]}}
        self.paths = ('archive', 'manifest', str(self.runtime))
        self.source_root = self.root.parent / 'source'
        self.commands = []

    def push(self, args, **kwargs):
        self.commands.append((args, kwargs))
        Path(args[args.index('--digestfile') + 1]).write_text(self.digest + '\n')
        return subprocess.CompletedProcess(args, 0)

    def context(self, after=None):
        return patch.object(publication, 'verified_record', side_effect=[self.record, after or self.record])

    def publish(self):
        return publication.publish(self.source_root, self.paths, self.root, 123, 1)

    def test_publishes_preserved_digest_then_rechecks_actual_registry_and_candidate(self):
        with self.context(), patch.object(publication.subprocess, 'run', side_effect=self.push), \
                patch.object(registry, 'bind_registry', return_value={'registryBindingDigest': 'bound'}) as bind:
            result = self.publish()
        args, options = self.commands[0]
        self.assertIn('--preserve-digests', args)
        self.assertIn('--dest-tls-verify=true', args)
        self.assertEqual(args[-1], 'docker://ghcr.io/flidai/leapview:nix-candidate-123-1')
        self.assertEqual(options['timeout'], 300)
        self.assertEqual(options['stderr'], subprocess.DEVNULL)
        self.assertEqual(result['image'], self.image)
        self.assertFalse(result['releaseAdmission'])
        bind.assert_called_once_with(self.image, 'application-image', [self.record], ['linux/amd64'])

    def test_input_or_spdx_changes_fail_before_publication(self):
        with patch.object(publication, 'verified_record', side_effect=ValueError('wrong source')), \
                patch.object(publication.subprocess, 'run') as run, self.assertRaises(ValueError):
            self.publish()
        run.assert_not_called()
        (self.runtime / 'sbom.spdx.json').write_text('{}')
        with self.context(), patch.object(publication.subprocess, 'run') as run, self.assertRaises(ValueError):
            self.publish()
        run.assert_not_called()

    def test_attestation_size_limit_is_enforced_before_publication(self):
        with self.context(), patch.object(publication, 'MAX_PREDICATE_BYTES', 4), \
                patch.object(publication.subprocess, 'run') as run, self.assertRaises(ValueError):
            self.publish()
        run.assert_not_called()

    def test_push_failure_or_digest_normalization_cannot_return_a_receipt(self):
        def wrong_digest(args, **kwargs):
            self.push(args, **kwargs)
            Path(args[args.index('--digestfile') + 1]).write_text('sha256:' + 'f' * 64)
        for transport in [wrong_digest, subprocess.CalledProcessError(1, ['skopeo'], stderr=b'token=secret')]:
            with self.subTest(transport=transport), self.context(), \
                    patch.object(publication.subprocess, 'run', side_effect=transport), \
                    patch.object(registry, 'bind_registry') as bind, self.assertRaises(ValueError) as error:
                self.publish()
            self.assertNotIn('secret', str(error.exception))
            bind.assert_not_called()

    def test_expired_or_changed_evidence_after_push_cannot_return_a_receipt(self):
        changed = copy.deepcopy(self.record)
        changed['candidateDigest'] = 'sha256:' + 'f' * 64
        for after in [changed, ValueError('expired')]:
            with self.subTest(after=after), self.context(after), \
                    patch.object(publication.subprocess, 'run', side_effect=self.push), \
                    patch.object(registry, 'bind_registry', return_value={'registryBindingDigest': 'bound'}), \
                    self.assertRaises(ValueError):
                self.publish()

    def test_unique_run_identity_and_amd64_scope_are_required(self):
        for run_id, attempt in [(0, 1), (1, 0), (True, 1), ('123', 1), (1, -1)]:
            with patch.object(publication.subprocess, 'run') as run, self.assertRaises(ValueError):
                publication.publish(self.source_root, self.paths, self.root, run_id, attempt)
            run.assert_not_called()
        self.record['artifact']['platform'] = 'linux/arm64'
        with self.context(), patch.object(publication.subprocess, 'run') as run, self.assertRaises(ValueError):
            self.publish()
        run.assert_not_called()

    def test_publisher_reads_candidate_identity_but_keeps_protected_policy_root(self):
        original = candidate.ROOT
        with patch.object(candidate, 'checkout_source', return_value=self.record['source']) as checkout, \
                patch.object(candidate, 'read_json_file', return_value={'manifest': 'data'}), \
                patch.object(candidate, 'verify', return_value=self.record) as verify:
            publication.verified_record(self.source_root, self.paths)
        checkout.assert_called_once_with(self.source_root)
        self.assertEqual(candidate.ROOT, original)
        self.assertEqual(verify.call_args.kwargs['runtime_dir'], self.runtime)

    def test_preparation_binds_content_without_registry_access_and_rechecks_inputs(self):
        with self.context(), patch.object(publication.oci, 'export_layout', return_value=self.digest), \
                patch.object(publication.subprocess, 'run') as run:
            result = publication.prepare(self.source_root, self.paths, self.root)
        self.assertEqual(result['oci']['digest'], self.digest)
        self.assertFalse(result['releaseAdmission'])
        run.assert_not_called()
        with self.context(ValueError('expired')), \
                patch.object(publication.oci, 'export_layout', return_value=self.digest), self.assertRaises(ValueError):
            publication.prepare(self.source_root, self.paths, self.root)

    def test_record_uses_protected_collector_and_candidate_source_identity(self):
        with patch.object(candidate, 'checkout_source', return_value=self.record['source']) as checkout, \
                patch.object(candidate, 'collect', return_value=self.record) as collect:
            result = publication.record(self.source_root, self.paths)
        checkout.assert_called_once_with(self.source_root)
        self.assertEqual(collect.call_args.args, (Path('archive'), publication.KIND, self.record['source']))
        self.assertEqual(collect.call_args.kwargs['runtime_dir'], self.runtime)
        self.assertEqual(result, self.record)

    def test_signatures_use_protected_event_revision_and_recheck_expiry(self):
        signer_revision = 'c' * 40
        publication.signed.validate_signer(publication.KIND, publication.WORKFLOW, signer_revision)
        with self.assertRaises(ValueError):
            publication.signed.validate_signer('site-image', publication.WORKFLOW, signer_revision)
        result = {'spdx': [{'predicateSHA256': candidate.digest_bytes(candidate.canonical_bytes(self.document))}]}
        with self.context(), patch.object(publication.signed, 'collect', return_value=result) as collect:
            self.assertEqual(publication.verify_signed(self.source_root, self.paths, self.image, signer_revision), result)
        self.assertEqual(collect.call_args.args[-2:], (publication.WORKFLOW, signer_revision))
        with self.context(ValueError('expired')), \
                patch.object(publication.signed, 'collect', return_value=result), self.assertRaises(ValueError):
            publication.verify_signed(self.source_root, self.paths, self.image, signer_revision)

    def test_cli_rejects_wrong_source_or_existing_receipt_without_publishing(self):
        output = self.root.parent / 'receipt.json'
        args = ['publish', '--source-root', str(self.source_root), '--source-revision', 'a' * 40,
                '--candidate', *self.paths, '--layout', str(self.root), '--run-id', '123', '--run-attempt', '1',
                '--output', str(output)]
        for source in [{'revision': 'f' * 40}, self.record['source']]:
            if source == self.record['source']:
                output.write_text('retained receipt')
            with patch.object(sys, 'argv', ['publication', *args]), \
                    patch.object(candidate, 'checkout_source', return_value=source), \
                    patch.object(publication, 'publish') as publish, self.assertRaises(SystemExit):
                publication.main()
            publish.assert_not_called()
        self.assertEqual(output.read_text(), 'retained receipt')

    @unittest.skipUnless(os.environ.get('LEAPVIEW_TEST_NIX_REGISTRY') == '1', 'requires locked Skopeo')
    def test_real_skopeo_publishes_over_tls_and_refetches_preserved_content(self):
        index = json.loads((self.root / 'index.json').read_text())
        index['manifests'][0]['annotations'] = {'org.opencontainers.image.ref.name': 'candidate'}
        (self.root / 'index.json').write_text(json.dumps(index))
        certificate, key = self.root.parent / 'certificate.pem', self.root.parent / 'key.pem'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                        '-subj', '/CN=localhost', '-addext', 'subjectAltName=IP:127.0.0.1',
                        '-keyout', str(key), '-out', str(certificate)],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        blobs, manifests, uploads, calls = {}, {}, {}, []
        identifiers = itertools.count()

        class Handler(BaseHTTPRequestHandler):
            def respond(self, code, data=b'', media='application/json', **headers):
                self.send_response(code)
                self.send_header('Content-Type', media)
                self.send_header('Content-Length', str(len(data)))
                self.send_header('Docker-Distribution-API-Version', 'registry/2.0')
                for name, value in headers.items():
                    self.send_header(name, value)
                self.end_headers()
                if self.command != 'HEAD':
                    self.wfile.write(data)

            def read_body(self):
                if self.headers.get('Transfer-Encoding') == 'chunked':
                    data = b''
                    while True:
                        size = int(self.rfile.readline().split(b';')[0].strip(), 16)
                        if not size:
                            self.rfile.readline()
                            return data
                        if len(data) + size > 1024 * 1024:
                            raise ValueError('fixture body too large')
                        data += self.rfile.read(size)
                        self.rfile.read(2)
                size = int(self.headers.get('Content-Length', 0))
                if size > 1024 * 1024:
                    raise ValueError('fixture body too large')
                return self.rfile.read(size)

            def do_HEAD(self):
                self.do_GET()

            def do_GET(self):
                calls.append((self.command, self.path))
                path = urlsplit(self.path).path
                if path == '/v2/':
                    self.respond(200, b'{}')
                elif path.startswith('/v2/fixture/blobs/') and path.rsplit('/', 1)[1] in blobs:
                    digest = path.rsplit('/', 1)[1]
                    self.respond(200, blobs[digest], 'application/octet-stream', **{'Docker-Content-Digest': digest})
                elif path.startswith('/v2/fixture/manifests/') and path.rsplit('/', 1)[1] in manifests:
                    data = manifests[path.rsplit('/', 1)[1]]
                    self.respond(200, data, publication.oci.MANIFEST,
                                 **{'Docker-Content-Digest': candidate.digest_bytes(data)})
                else:
                    self.respond(404)

            def do_POST(self):
                calls.append(('POST', self.path))
                location = '/v2/fixture/blobs/uploads/' + str(next(identifiers))
                uploads[location] = self.read_body()
                self.respond(202, **{'Location': location, 'Range': '0-0'})

            def do_PATCH(self):
                calls.append(('PATCH', self.path))
                path = urlsplit(self.path).path
                uploads[path] += self.read_body()
                self.respond(202, **{'Location': path, 'Range': '0-' + str(max(0, len(uploads[path]) - 1))})

            def do_PUT(self):
                calls.append(('PUT', self.path))
                url = urlsplit(self.path)
                data = self.read_body()
                if '/blobs/uploads/' in url.path:
                    data = uploads.pop(url.path) + data
                    digest = parse_qs(url.query)['digest'][0]
                    if digest != candidate.digest_bytes(data):
                        self.respond(400)
                        return
                    blobs[digest] = data
                    self.respond(201, **{'Location': '/v2/fixture/blobs/' + digest, 'Docker-Content-Digest': digest})
                elif '/manifests/' in url.path:
                    digest = candidate.digest_bytes(data)
                    manifests[url.path.rsplit('/', 1)[1]] = data
                    manifests[digest] = data
                    self.respond(201, **{'Docker-Content-Digest': digest})
                else:
                    self.respond(404)

            def log_message(self, *_):
                pass

        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(certificate, key)
        server.socket = context.wrap_socket(server.socket, server_side=True)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        repository = f'127.0.0.1:{server.server_port}/fixture'
        try:
            with self.context(), patch.dict(registry.REPOSITORIES, {publication.KIND: repository}), \
                    patch.dict(os.environ, {'SSL_CERT_FILE': str(certificate)}):
                result = self.publish()
            self.assertEqual(result['image'], repository + '@' + self.digest)
            self.assertFalse(result['releaseAdmission'])
            self.assertIn(('PUT', '/v2/fixture/manifests/nix-candidate-123-1'), calls)
            self.assertIn(('GET', '/v2/fixture/manifests/' + self.digest), calls)
            self.assertEqual(set(blobs), {self.record['artifact']['configDigest'], self.record['artifact']['layerDiffIDs'][0]})
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)


if __name__ == '__main__':
    unittest.main()
