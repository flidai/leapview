"""Read-only delivery-generation status polling contract."""
import io
import json
import pathlib
import sys
import unittest
from unittest.mock import patch
from urllib.error import URLError

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
import demo_client_contract as generation_status


PROJECT = 'project:historical-transition'
GENERATION = 'generation:published'
CANDIDATE = 'candidate:published'
TARGET_ID = 'instance:demo'
ENVIRONMENT = 'production'
TOKEN = 'already-issued-release-token'


def body(status='active', **overrides):
    value = {
        'id': GENERATION,
        'projectId': PROJECT,
        'candidateId': CANDIDATE,
        'targetId': TARGET_ID,
        'environment': ENVIRONMENT,
        'status': status,
    }
    value.update(overrides)
    return json.dumps(value).encode()


class GenerationStatusPollTests(unittest.TestCase):
    def make_poller(self, responses, timeout=10, poll_interval=2):
        calls = []
        sleeps = []
        diagnostics = []
        now = [0.0]

        def read_status(target, project, generation, token, request_timeout,
                        ca_cert=None, proxy=None):
            calls.append((target, project, generation, token, request_timeout, ca_cert, proxy))
            if not responses:
                raise AssertionError('unexpected extra generation status request')
            response = responses.pop(0)
            if len(response) == 3:
                return response
            status, response_body = response
            return status, response_body, 'application/problem+json' if status == 503 else 'application/json'

        def sleep(seconds):
            sleeps.append(seconds)
            now[0] += seconds

        def monotonic():
            return now[0]

        def run():
            return generation_status.wait_for_generation(
                'https://demo.leapview.dev', PROJECT, GENERATION, CANDIDATE, TOKEN,
                timeout_seconds=timeout, poll_interval_seconds=poll_interval,
                target_id=TARGET_ID, environment=ENVIRONMENT,
                ca_cert='/tmp/test-ca.pem', proxy='http://127.0.0.1:43210',
                read_status=read_status, monotonic=monotonic, sleep=sleep,
                diagnostic=diagnostics.append)

        return run, calls, sleeps, diagnostics

    def test_exact_transient_503_then_active_succeeds_with_same_read_token(self):
        responses = [
            (503, json.dumps({'code': 'DELIVERY_READ_UNAVAILABLE'}).encode()),
            (200, body()),
        ]
        run, calls, sleeps, diagnostics = self.make_poller(responses)
        result = run()
        self.assertEqual(result['status'], 'active')
        self.assertEqual(len(calls), 2)
        self.assertEqual([call[3] for call in calls], [TOKEN, TOKEN])
        self.assertEqual(sleeps, [2])
        self.assertEqual(len(diagnostics), 1)
        self.assertIn("Content-Type='application/problem+json'", diagnostics[0])
        self.assertIn('DELIVERY_READ_UNAVAILABLE', diagnostics[0])

    def test_exact_plaintext_service_unavailable_503_then_active_succeeds(self):
        run, calls, sleeps, diagnostics = self.make_poller([
            (503, b' Service Unavailable\r\n', 'text/plain'),
            (200, body(), 'application/json'),
        ])
        self.assertEqual(run()['status'], 'active')
        self.assertEqual(len(calls), 2)
        self.assertEqual([call[3] for call in calls], [TOKEN, TOKEN])
        self.assertEqual(sleeps, [2])
        self.assertEqual(len(diagnostics), 1)
        self.assertIn("Content-Type='text/plain'", diagnostics[0])
        self.assertIn('Service Unavailable', diagnostics[0])

    def test_plaintext_503_retries_only_exact_content_type_and_body(self):
        for content_type, response_body in (
                ('text/plain', b'Service unavailable'),
                ('text/plain', b'Service Temporarily Unavailable'),
                ('text/html', b'Service Unavailable'),
                ('text/plain; charset=utf-8', b'Service Unavailable')):
            with self.subTest(content_type=content_type, response_body=response_body):
                run, calls, sleeps, _ = self.make_poller([
                    (503, response_body, content_type),
                    (200, body(), 'application/json'),
                ])
                with self.assertRaisesRegex(generation_status.GenerationStatusError, 'not retryable'):
                    run()
                self.assertEqual(len(calls), 1)
                self.assertEqual(sleeps, [])

    def test_prepared_generation_then_active_succeeds(self):
        responses = [(200, body(status='prepared')), (200, body())]
        run, calls, sleeps, _ = self.make_poller(responses)
        self.assertEqual(run()['status'], 'active')
        self.assertEqual(len(calls), 2)
        self.assertEqual(sleeps, [2])

    def test_forbidden_and_not_found_fail_immediately_with_safe_response_details(self):
        for status in (403, 404):
            with self.subTest(status=status):
                error_body = json.dumps({'code': 'GENERATION_READ_DENIED',
                                         'detail': 'token=' + TOKEN}).encode()
                run, calls, sleeps, _ = self.make_poller([(status, error_body), (200, body())])
                with self.assertRaisesRegex(generation_status.GenerationStatusError,
                                            'HTTP {} Content-Type'.format(status)) as raised:
                    run()
                self.assertEqual(len(calls), 1)
                self.assertEqual(sleeps, [])
                self.assertIn('GENERATION_READ_DENIED', str(raised.exception))
                self.assertIn('[redacted]', str(raised.exception))
                self.assertNotIn(TOKEN, str(raised.exception))

    def test_unrelated_503_failure_code_fails_immediately(self):
        run, calls, sleeps, _ = self.make_poller([
            (503, json.dumps({'code': 'SOME_OTHER_FAILURE'}).encode()),
            (200, body()),
        ])
        with self.assertRaisesRegex(generation_status.GenerationStatusError, 'not retryable'):
            run()
        self.assertEqual(len(calls), 1)
        self.assertEqual(sleeps, [])

    def test_missing_code_503_fails_with_bounded_redacted_diagnostic(self):
        run, calls, sleeps, diagnostics = self.make_poller([
            (503, json.dumps({'message': 'token=' + TOKEN, 'payload': 'x' * 5000}).encode()),
            (200, body()),
        ])
        with self.assertRaisesRegex(generation_status.GenerationStatusError, 'not retryable') as raised:
            run()
        self.assertEqual(len(calls), 1)
        self.assertEqual(sleeps, [])
        self.assertIn('Content-Type', str(raised.exception))
        self.assertIn('[redacted]', str(raised.exception))
        self.assertNotIn(TOKEN, str(raised.exception))
        self.assertLess(len(str(raised.exception)), 2300)
        self.assertEqual(len(diagnostics), 1)

    def test_deadline_exhaustion_is_bounded(self):
        run, calls, sleeps, _ = self.make_poller(
            [(200, body(status='prepared'))] * 4, timeout=3, poll_interval=2)
        with self.assertRaisesRegex(generation_status.GenerationStatusError, 'bounded deadline'):
            run()
        self.assertEqual(len(calls), 2)
        self.assertEqual(sleeps, [2, 1])

    def test_transport_failure_reports_reason_once_without_retrying(self):
        def fail_read(*_args, **_kwargs):
            raise URLError('TLS handshake failed for ' + TOKEN)

        diagnostics = []
        with self.assertRaisesRegex(generation_status.GenerationStatusError, 'TLS handshake failed') as raised:
            generation_status.wait_for_generation(
                'https://demo.leapview.dev', PROJECT, GENERATION, CANDIDATE, TOKEN,
                timeout_seconds=10, poll_interval_seconds=2, target_id=TARGET_ID,
                environment=ENVIRONMENT, read_status=fail_read,
                monotonic=lambda: 0, sleep=lambda _seconds: self.fail('transport failures must not retry'),
                diagnostic=diagnostics.append)
        self.assertNotIn(TOKEN, str(raised.exception))
        self.assertEqual(diagnostics, [])

    def test_wrong_generation_project_or_candidate_fails_immediately(self):
        for field, value in (
                ('id', 'generation:other'),
                ('projectId', 'project:other'),
                ('candidateId', 'candidate:other')):
            with self.subTest(field=field):
                run, calls, sleeps, _ = self.make_poller([
                    (200, body(**{field: value})), (200, body())])
                with self.assertRaisesRegex(generation_status.GenerationStatusError, 'identity mismatch'):
                    run()
                self.assertEqual(len(calls), 1)
                self.assertEqual(sleeps, [])

    def test_http_request_is_one_authenticated_get_without_redirect_following(self):
        class Response(io.BytesIO):
            headers = {'Content-Type': 'application/json'}

            def getcode(self):
                return 200

        class Opener:
            def __init__(self):
                self.requests = []

            def open(self, request, timeout):
                self.requests.append((request, timeout))
                return Response(body())

        opener = Opener()
        with patch.object(generation_status, 'build_opener', return_value=opener):
            status, response, content_type = generation_status.read_generation_status(
                'https://demo.leapview.dev', PROJECT, GENERATION, TOKEN, 5,
                proxy='http://127.0.0.1:43210')
        self.assertEqual(content_type, 'application/json')
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(response)['id'], GENERATION)
        self.assertEqual(len(opener.requests), 1)
        request, timeout = opener.requests[0]
        self.assertEqual(request.get_method(), 'GET')
        self.assertEqual(request.get_header('Authorization'), 'Bearer ' + TOKEN)
        self.assertEqual(timeout, 5)
        self.assertIsNone(request.data)
        self.assertEqual(request.full_url,
                         'https://demo.leapview.dev/api/v1/projects/project:historical-transition/'
                         'delivery/generations/generation%3Apublished')


if __name__ == '__main__':
    unittest.main()
