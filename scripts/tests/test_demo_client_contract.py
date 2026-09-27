import http.server
import io
import json
import os
import pathlib
import shutil
import socket
import socketserver
import ssl
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

SCRIPTS = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import demo_client_contract as contract
import demo_compose_deploy as compose
import demo_upgrade_transport as upgrade


class CountingSentinel(socketserver.BaseRequestHandler):
    def handle(self):
        self.server.hits += 1
        self.request.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok')


class CountingTCPServer(socketserver.ThreadingTCPServer):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.hits = 0

    def get_request(self):
        request, address = super().get_request()
        self.hits += 1
        return request, address


class ClientContractTests(unittest.TestCase):
    def test_profiles_use_exact_publisher_reviewer_and_inspection_actions(self):
        self.assertEqual(contract.scope_for(contract.LEGACY_PROFILE, 'publisher'),
                         'RESOURCE_USE RESOURCE_READ RESOURCE_EDIT RESOURCE_PUBLISH')
        # The old approval/status/evidence GET routes require RESOURCE_READ;
        # OAuth scopes are exact and PROJECT_ADMIN does not imply it.
        self.assertEqual(contract.scope_for(contract.LEGACY_PROFILE, 'release'),
                         'PROJECT_ADMIN RESOURCE_READ')
        self.assertEqual(contract.scope_for(contract.TYPED_PROFILE, 'publisher'),
                         'connection.manage connection.use delivery.build delivery.plan delivery.publish delivery.read model.read semantic.consume source.read')
        self.assertEqual(contract.scope_for(contract.TYPED_PROFILE, 'release'),
                         'delivery.approve delivery.read')
        self.assertEqual(contract.scope_for(contract.TYPED_PROFILE, 'inspection'), 'delivery.read')

    def test_typed_publisher_scope_includes_graph_reads_without_authoring_or_approval(self):
        actions = set(contract.scope_for(contract.TYPED_PROFILE, 'publisher').split())
        self.assertTrue({'source.read', 'model.read', 'semantic.consume', 'connection.use'} <= actions)
        self.assertFalse({'source.update', 'model.update', 'semantic.update', 'connection.create',
                          'delivery.approve', 'project.access.manage'} & actions)

    def test_unknown_profile_and_role_fail_without_legacy_retry(self):
        for profile, role in [('future/v2', 'publisher'), (contract.TYPED_PROFILE, 'administrator')]:
            with self.subTest(profile=profile, role=role), self.assertRaises(ValueError):
                contract.scope_for(profile, role)

    def test_predecessor_profile_is_explicit_and_a_denial_is_not_retried(self):
        env = {
            'DEMO_PERMISSION_PROFILE': contract.TYPED_PROFILE,
            'DEMO_PUBLISHER_CLIENT_ID': 'publisher',
            'DEMO_PUBLISHER_CLIENT_SECRET': 'secret',
            'DEMO_PROJECT_ID': 'project',
        }
        replies = [io.BytesIO(b'{"access_token":"legacy-token"}'),
                   io.BytesIO(json.dumps({'buildRevision':'a'*40,'buildDirty':False}).encode())]
        with patch.object(compose, 'demo_urlopen', side_effect=replies) as request:
            compose.verify_public_revision('a'*40, contract.LEGACY_PROFILE, environ=env)
        form = request.call_args_list[0].args[0].data.decode()
        self.assertIn('scope=RESOURCE_READ', form)
        self.assertNotIn('dashboard.read', form)

        with patch.object(compose, 'demo_urlopen', side_effect=RuntimeError('403 denied')) as denied:
            with self.assertRaisesRegex(RuntimeError, '403 denied'):
                compose.verify_public_revision('a'*40, contract.LEGACY_PROFILE, environ=env)
            denied.assert_called_once()

    def test_clone_environment_removes_both_case_bypass_values_and_pins_clients(self):
        env = contract.clone_only_environment({
            'HTTPS_PROXY': 'http://attacker.invalid:8888', 'http_proxy': 'http://bad:9',
            'NO_PROXY': 'demo.leapview.dev', 'no_proxy': 'demo.leapview.dev', 'PATH': '/bin',
        }, 'http://127.0.0.1:12345')
        self.assertEqual(env['HTTPS_PROXY'], 'http://127.0.0.1:12345')
        self.assertEqual(env['https_proxy'], 'http://127.0.0.1:12345')
        self.assertEqual(env['NO_PROXY'], '')
        self.assertEqual(env['no_proxy'], '')
        self.assertEqual(env['DEMO_TARGET'], contract.CLONE_TARGET)
        self.assertEqual(env['PATH'], '/bin')
        with self.assertRaises(ValueError):
            contract.validate_client_environment(env)
        env['DEMO_PERMISSION_PROFILE'] = contract.TYPED_PROFILE
        self.assertEqual(contract.validate_client_environment(env), contract.TYPED_PROFILE)

    def test_rehearsal_python_request_uses_only_local_proxy_when_direct_sentinel_exists(self):
        with CountingTCPServer(('127.0.0.1', 0), CountingSentinel) as sentinel, \
             socket.socket() as unavailable_upstream:
            # Reserve and close the port so the allowed proxy response is a
            # deterministic 502 without opening any external connection.
            unavailable_upstream.bind(('127.0.0.1', 0))
            upstream_port = unavailable_upstream.getsockname()[1]
            unavailable_upstream.close()
            with CountingTCPServer(('127.0.0.1', 0), upgrade.DemoTunnelProxy) as proxy:
                proxy.daemon_threads = True
                proxy.upstream_port = upstream_port
                thread = threading.Thread(target=proxy.serve_forever, daemon=True)
                thread.start()
                env = contract.clone_only_environment({
                    'DEMO_PERMISSION_PROFILE': contract.TYPED_PROFILE,
                    'HTTPS_PROXY': 'http://attacker.invalid:8123',
                    'NO_PROXY': 'demo.leapview.dev', 'no_proxy': 'demo.leapview.dev',
                }, f'http://127.0.0.1:{proxy.server_address[1]}')
                # If an implementation bypasses the proxy, direct resolution
                # lands on this local sentinel instead of the public network.
                real_create_connection = socket.create_connection

                def local_only_create_connection(address, *args, **kwargs):
                    if address == ('demo.leapview.dev', 443):
                        address = sentinel.server_address
                    return real_create_connection(address, *args, **kwargs)

                try:
                    with patch.dict('os.environ', NO_PROXY='*', no_proxy='demo.leapview.dev'), \
                         patch.object(socket, 'create_connection', side_effect=local_only_create_connection):
                        request = __import__('urllib.request').request.Request(contract.CLONE_TARGET + '/oauth/token')
                        with self.assertRaises(Exception):
                            compose.demo_urlopen(request, timeout=2, environ=env)
                    self.assertEqual(sentinel.hits, 0)
                    self.assertGreater(proxy.hits, 0)
                finally:
                    proxy.shutdown()
                    thread.join(timeout=5)

    def test_python_redirects_are_rejected(self):
        class Redirect(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(302)
                self.send_header('Location', 'https://public-sentinel.invalid/next')
                self.end_headers()
            def log_message(self, *_args):
                pass

        with http.server.ThreadingHTTPServer(('127.0.0.1', 0), Redirect) as server:
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                request = __import__('urllib.request').request.Request(f'http://127.0.0.1:{server.server_port}/')
                with self.assertRaises(RuntimeError):
                    compose.demo_urlopen(request, timeout=2, environ={'DEMO_PERMISSION_PROFILE': contract.TYPED_PROFILE})
            finally:
                server.shutdown()
                thread.join(timeout=5)

    def test_curl_and_go_requests_stay_on_the_local_proxy(self):
        curl = shutil.which('curl')
        go = shutil.which('go')
        openssl = shutil.which('openssl')
        if not curl or not go or not openssl:
            self.skipTest('curl, Go, and OpenSSL are required for the deployed publication client')

        class RedirectOrFail(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.server.paths.append(self.path)
                if self.path == '/go':
                    self.send_response(302)
                    self.send_header('Location', 'https://public-sentinel.invalid/egress-probe')
                else:
                    self.send_response(502)
                self.send_header('Content-Length', '0')
                self.end_headers()
            def log_message(self, *_args):
                pass

        with tempfile.TemporaryDirectory() as certdir:
            key, certificate = pathlib.Path(certdir) / 'key.pem', pathlib.Path(certdir) / 'cert.pem'
            subprocess.run([openssl, 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                            '-subj', '/CN=demo.leapview.dev', '-keyout', str(key), '-out', str(certificate)],
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
            origin = http.server.ThreadingHTTPServer(('127.0.0.1', 0), RedirectOrFail)
            origin.paths = []
            tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            tls.load_cert_chain(str(certificate), str(key))
            origin.socket = tls.wrap_socket(origin.socket, server_side=True)
            origin_thread = threading.Thread(target=origin.serve_forever, daemon=True)
            origin_thread.start()
            with CountingTCPServer(('127.0.0.1', 0), CountingSentinel) as sentinel, \
                 CountingTCPServer(('127.0.0.1', 0), upgrade.DemoTunnelProxy) as proxy:
                proxy.daemon_threads = True
                proxy.upstream_port = origin.server_port
                proxy_thread = threading.Thread(target=proxy.serve_forever, daemon=True)
                sentinel_thread = threading.Thread(target=sentinel.serve_forever, daemon=True)
                proxy_thread.start()
                sentinel_thread.start()
                env = contract.clone_only_environment({
                    'PATH': os.environ.get('PATH', ''),
                    'DEMO_PERMISSION_PROFILE': contract.TYPED_PROFILE,
                    'NO_PROXY': '*', 'no_proxy': 'demo.leapview.dev',
                    'REQUEST_METHOD': 'GET',
                }, f'http://127.0.0.1:{proxy.server_address[1]}')
                env['DEMO_SENTINEL'] = f'127.0.0.1:{sentinel.server_address[1]}'
                try:
                    curl_result = subprocess.run([
                        curl, '--insecure', '--fail', '--silent', '--show-error', '--max-time', '3',
                        '--proxy', env['DEMO_CLONE_PROXY'], '--noproxy', '',
                        '--proto', '=https', '--proto-redir', '=https', '--max-redirs', '0',
                        'https://demo.leapview.dev/curl',
                    ], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
                    self.assertNotEqual(curl_result.returncode, 0)
                    self.assertGreater(proxy.hits, 0, curl_result.stderr.decode(errors='replace'))
                    self.assertEqual(sentinel.hits, 0)

                    source = r'''package main
import (
    "context"
    "crypto/tls"
    "net"
    "net/http"
    "os"
    "time"
)
func main() {
    transport := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
        if address == "demo.leapview.dev:443" || address == "public-sentinel.invalid:443" { address = os.Getenv("DEMO_SENTINEL") }
        return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
    }}
    client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
    response, err := client.Get("https://demo.leapview.dev/go")
    if err != nil { return }
    if response.StatusCode != 403 { os.Exit(3) }
    response.Body.Close()
}
'''
                    with tempfile.TemporaryDirectory() as directory:
                        env['HOME'] = directory
                        env['GOCACHE'] = str(pathlib.Path(directory) / 'go-cache')
                        pathlib.Path(env['GOCACHE']).mkdir()
                        main_file = pathlib.Path(directory) / 'main.go'
                        main_file.write_text(source)
                        go_result = subprocess.run([go, 'run', str(main_file)], env=env,
                                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                                   timeout=60, check=False)
                    self.assertEqual(go_result.returncode, 0, go_result.stderr.decode(errors='replace'))
                    self.assertGreater(proxy.hits, 1, go_result.stderr.decode(errors='replace'))
                    self.assertEqual(sentinel.hits, 0)
                    self.assertEqual(origin.paths, ['/curl', '/go'])
                finally:
                    proxy.shutdown()
                    sentinel.shutdown()
                    proxy_thread.join(timeout=5)
                    sentinel_thread.join(timeout=5)
                    origin.shutdown()
                    origin_thread.join(timeout=5)
                    origin.server_close()


if __name__ == '__main__':
    unittest.main()
