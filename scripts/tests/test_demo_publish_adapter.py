"""Execution contract between deploy_demo.sh and the standalone publish CLI."""
import contextlib
import http.server
import os
import pathlib
import shutil
import subprocess
import tempfile
import threading
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]
REVISION = 'b' * 40
PROJECT = 'project:historical-transition'
PUBLICATION = 'publication:demo'
CANDIDATE = 'candidate:demo'
GENERATION = 'generation:demo'
TARGET = 'target:from-publication-evidence'
ENVIRONMENT = 'prod'


@contextlib.contextmanager
def readiness_server(statuses):
    paths = []

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            paths.append(self.path)
            index = min(len(paths) - 1, len(statuses) - 1)
            status = statuses[index] if self.path == '/readyz' else 404
            self.send_response(status)
            self.end_headers()
            self.wfile.write(b'ok\n')

        def log_message(self, *_):
            pass

    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f'http://127.0.0.1:{server.server_port}/readyz', paths
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


class PublishAdapterTests(unittest.TestCase):
    def test_minimal_publish_result_uses_committed_evidence_for_target_identity(self):
        result = self.run_publish_adapter()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn('published source ' + REVISION, result.stdout)

    def test_readiness_retries_transient_503_until_same_endpoint_is_ready(self):
        with readiness_server([503, 200]) as (ready_url, paths):
            result = self.run_publish_adapter(ready_url)
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn('published source ' + REVISION, result.stdout)
        self.assertEqual(paths, ['/readyz', '/readyz'])

    def test_readiness_does_not_retry_unauthorized_response(self):
        with readiness_server([401, 200]) as (ready_url, paths):
            result = self.run_publish_adapter(ready_url)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual(paths, ['/readyz'])

    def run_publish_adapter(self, ready_url=None):
        with tempfile.TemporaryDirectory() as temp:
            temp_root = pathlib.Path(temp)
            bin_dir = temp_root / 'bin'
            bin_dir.mkdir()
            source_root = temp_root / 'source'
            source_root.mkdir()
            data_root = temp_root / 'data'
            data_root.mkdir()

            fake_cli = temp_root / 'leapview'
            fake_cli.write_text(r'''#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == "api" && "$2" == "call" ]]; then
  case "$3" in
    getCapabilities)
      jq -n --arg revision "$DEMO_SOURCE_REVISION" '{apiVersion:"v1",deliveryMode:"native_postgres",buildRevision:$revision,buildDirty:false,buildDevelopment:true}'
      ;;
    getDeliveryCandidateStatus)
      printf '%s\n' '{"status":"ready"}'
      ;;
    requestDeliveryPublicationApproval)
      printf '%s\n' '{"id":"approval:demo","revision":1,"status":"pending"}'
      ;;
    approveDeliveryPublicationApproval|getDeliveryPublicationApproval)
      printf '%s\n' '{"status":"approved"}'
      ;;
    getDeliveryPublicationEvidence)
      jq -n --arg publication "$TEST_EXPECTED_PUBLICATION" \
        --arg project "$TEST_EXPECTED_PROJECT" --arg candidate "$TEST_EXPECTED_CANDIDATE" \
        --arg generation "$TEST_EXPECTED_GENERATION" --arg target "$TEST_EXPECTED_TARGET_ID" \
        --arg environment "$TEST_EXPECTED_ENVIRONMENT" \
        '{id:$publication,projectId:$project,candidateId:$candidate,generationId:$generation,targetId:$target,environment:$environment,status:"committed"}'
      ;;
    *) echo "unexpected fake LeapView API operation: $3" >&2; exit 70 ;;
  esac
  exit 0
fi
case "$1" in
  data) exit 0 ;;
  plan)
    jq -n --arg project "$TEST_EXPECTED_PROJECT" '{planId:"plan:demo",projectId:$project,status:"planned"}'
    ;;
  build)
    jq -n --arg candidate "$TEST_EXPECTED_CANDIDATE" '{status:"sealed",candidateId:$candidate}'
    ;;
  publish)
    # This is the actual projectcli.PublishResult shape. In particular, it
    # has no targetId or environment fields.
    jq -n --arg publication "$TEST_EXPECTED_PUBLICATION" \
      --arg candidate "$TEST_EXPECTED_CANDIDATE" --arg generation "$TEST_EXPECTED_GENERATION" \
      '{schemaVersion:1,publicationId:$publication,generationId:$generation,candidateId:$candidate,planId:"plan:demo",planDigest:"sha256:test",status:"pending",targetRevision:2}'
    ;;
  *) echo "unexpected fake LeapView command: $*" >&2; exit 70 ;;
esac
''')
            fake_cli.chmod(0o755)

            fake_go = bin_dir / 'go'
            fake_go.write_text(r'''#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  build)
    while (($#)); do
      if [[ "$1" == "-o" ]]; then
        shift
        cp "$TEST_FAKE_LEAPVIEW" "$1"
        chmod 0700 "$1"
        exit 0
      fi
      shift
    done
    echo "fake go build did not receive -o" >&2
    exit 70
    ;;
  run) exit 0 ;;
  *) echo "unexpected fake go command: $*" >&2; exit 70 ;;
esac
''')
            fake_go.chmod(0o755)

            fake_python = bin_dir / 'python3'
            fake_python.write_text(r'''#!/usr/bin/env bash
set -euo pipefail
script="$1"
if [[ "$script" == */scripts/demo_client_contract.py ]]; then
  shift
  case "$1" in
    --profile)
      role=""
      while (($#)); do
        if [[ "$1" == "--role" ]]; then role="$2"; break; fi
        shift
      done
      case "$role" in
        publisher) printf '%s\n' 'connection.upload connection.read connection.use delivery.build delivery.plan delivery.publish delivery.read model.read semantic.consume source.read' ;;
        release) printf '%s\n' 'delivery.approve delivery.read' ;;
        *) echo "unexpected client role: $role" >&2; exit 70 ;;
      esac
      ;;
    --validate-environment) exit 0 ;;
    --wait-generation)
      target_id=""
      environment=""
      while (($#)); do
        case "$1" in
          --target-id) target_id="$2"; shift 2 ;;
          --environment) environment="$2"; shift 2 ;;
          *) shift ;;
        esac
      done
      [[ "$target_id" == "$TEST_EXPECTED_TARGET_ID" ]] || {
        echo "generation poll target came from the wrong response: $target_id" >&2; exit 71;
      }
      [[ "$environment" == "$TEST_EXPECTED_ENVIRONMENT" ]] || {
        echo "generation poll environment came from the wrong response: $environment" >&2; exit 72;
      }
      jq -n --arg project "$TEST_EXPECTED_PROJECT" --arg candidate "$TEST_EXPECTED_CANDIDATE" \
        --arg generation "$TEST_EXPECTED_GENERATION" --arg target "$target_id" \
        --arg environment "$environment" \
        '{id:$generation,projectId:$project,candidateId:$candidate,targetId:$target,environment:$environment,status:"active"}'
      ;;
    *) echo "unexpected client contract invocation: $*" >&2; exit 70 ;;
  esac
  exit 0
fi
exec /usr/bin/python3 "$script" "$@"
''')
            fake_python.chmod(0o755)

            fake_curl = bin_dir / 'curl'
            fake_curl.write_text(r'''#!/usr/bin/env bash
set -euo pipefail
for url in "$@"; do :; done
case "$url" in
  */oauth/token)
    client_id=""
    scope=""
    while (($#)); do
      if [[ "$1" == "--data-urlencode" && $# -ge 2 ]]; then
        case "$2" in
          client_id=*) client_id="${2#client_id=}" ;;
          scope=*) scope="${2#scope=}" ;;
        esac
        shift 2
      else
        shift
      fi
    done
    case "$client_id" in
      publisher-client) expected_scope="$TEST_EXPECTED_PUBLISHER_SCOPE" ;;
      release-client) expected_scope="$TEST_EXPECTED_RELEASE_SCOPE" ;;
      *) echo "unexpected OAuth client: $client_id" >&2; exit 71 ;;
    esac
    [[ "$scope" == "$expected_scope" ]] || {
      echo "OAuth scope for $client_id did not match profile: $scope" >&2; exit 72;
    }
    printf '%s\n' '{"access_token":"fake-workload-token"}'
    ;;
  */readyz)
    if [[ -n "${TEST_READY_URL:-}" ]]; then
      args=()
      while (($#)); do
        case "$1" in
          --proxy|--noproxy|--proto|--proto-redir|--max-redirs) shift 2 ;;
          */readyz) args+=("$TEST_READY_URL"); shift ;;
          *) args+=("$1"); shift ;;
        esac
      done
      exec "$TEST_REAL_CURL" "${args[@]}" --noproxy '*'
    fi
    printf '%s\n' 'ok'
    ;;
  */login) printf '%s\n' '<title>LeapView Login</title>' ;;
  */)
    printf '%s\n%s\n' '302' "$TEST_EXPECTED_TARGET$TEST_EXPECTED_LOGIN_PATH"
    ;;
  *) echo "unexpected fake curl URL: $url" >&2; exit 70 ;;
esac
''')
            fake_curl.chmod(0o755)

            env = os.environ.copy()
            env.update({
                'PATH': str(bin_dir) + os.pathsep + env['PATH'],
                'TEST_FAKE_LEAPVIEW': str(fake_cli),
                'TEST_EXPECTED_PROJECT': PROJECT,
                'TEST_EXPECTED_PUBLICATION': PUBLICATION,
                'TEST_EXPECTED_CANDIDATE': CANDIDATE,
                'TEST_EXPECTED_GENERATION': GENERATION,
                'TEST_EXPECTED_TARGET_ID': TARGET,
                'TEST_EXPECTED_ENVIRONMENT': ENVIRONMENT,
                'TEST_EXPECTED_PUBLISHER_SCOPE': 'connection.upload connection.read connection.use delivery.build delivery.plan delivery.publish delivery.read model.read semantic.consume source.read',
                'TEST_EXPECTED_RELEASE_SCOPE': 'delivery.approve delivery.read',
                'TEST_EXPECTED_TARGET': 'https://demo.leapview.dev',
                'TEST_EXPECTED_LOGIN_PATH': '/login',
                'DEMO_TARGET': 'https://demo.leapview.dev',
                'DEMO_DATASET': 'cfo',
                'DEMO_SOURCE_REVISION': REVISION,
                'DEMO_PROJECT_ID': PROJECT,
                'DEMO_PUBLISHER_CLIENT_ID': 'publisher-client',
                'DEMO_PUBLISHER_CLIENT_SECRET': 'publisher-secret',
                'DEMO_RELEASE_CLIENT_ID': 'release-client',
                'DEMO_RELEASE_CLIENT_SECRET': 'release-secret',
                'DEMO_PERMISSION_PROFILE': 'leapview.permissions/v1',
                'DEMO_CLONE_ONLY': '1',
                'DEMO_CLONE_PROXY': 'http://127.0.0.1:43210',
                'DEMO_FIXTURE_SOURCE_ROOT': str(source_root),
                'DEMO_FIXTURE_DATA_PATH': str(data_root),
            })
            if ready_url is not None:
                real_curl = shutil.which('curl')
                self.assertIsNotNone(real_curl)
                env.update({
                    'TEST_READY_URL': ready_url,
                    'TEST_REAL_CURL': real_curl,
                })

            result = subprocess.run(
                ['bash', str(ROOT / 'scripts' / 'deploy_demo.sh')],
                cwd=ROOT, env=env, text=True, stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT, timeout=30, check=False)
            return result


if __name__ == '__main__':
    unittest.main()
