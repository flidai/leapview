#!/usr/bin/env python3
"""Explicit OAuth and transport contracts for the hosted demo clients."""
import argparse
from contextlib import closing
import json
import os
from pathlib import Path
import re
import ssl
import sys
import time
from urllib.error import HTTPError
from urllib.parse import quote, urlsplit, urlunsplit
from urllib.request import (HTTPRedirectHandler, HTTPSHandler, ProxyHandler,
                            Request, build_opener)

LEGACY_PROFILE = 'legacy-capabilities/v1'
TYPED_PROFILE = 'leapview.permissions/v1'
PROFILES = {
    LEGACY_PROFILE: {
        'publisher': 'RESOURCE_USE RESOURCE_READ RESOURCE_EDIT RESOURCE_PUBLISH',
        # Approval decisions require PROJECT_ADMIN; approval status,
        # publication evidence and generation reads require explicit READ.
        # Legacy workload scopes do not inherit one capability from another.
        'release': 'PROJECT_ADMIN RESOURCE_READ',
        'inspection': 'RESOURCE_READ',
    },
    TYPED_PROFILE: {
        # deploy_demo.sh: managed-data upload and upload-session reads, source sync, plan,
        # build, graph-derived dependency authorization, candidate status,
        # publication and approval request. Exact resource authority still
        # comes from the target's typed grants; these actions are only the
        # short-lived token ceiling for the project.
        'publisher': 'connection.manage connection.read connection.use delivery.build delivery.plan delivery.publish delivery.read model.read semantic.consume source.read',
        # Keep approval separate from publishing; reads are limited to the
        # resulting publication/generation. Their evidence binds ProjectUID.
        'release': 'delivery.approve delivery.read',
        'inspection': 'delivery.read',
    },
}

PROXY_NAMES = ('HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY',
               'http_proxy', 'https_proxy', 'all_proxy')
BYPASS_NAMES = ('NO_PROXY', 'no_proxy')
CLONE_TARGET = 'https://demo.leapview.dev'
TRANSIENT_READ_CODE = 'DELIVERY_READ_UNAVAILABLE'
MAX_RESPONSE_BYTES = 1 << 20
DEFAULT_GENERATION_TIMEOUT_SECONDS = 90.0
DEFAULT_GENERATION_POLL_INTERVAL_SECONDS = 2.0


class GenerationStatusError(RuntimeError):
    """The requested generation did not reach the exact active state."""


class _NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _target_origin(target):
    value = str(target or '').strip()
    parsed = urlsplit(value)
    if (parsed.scheme != 'https' or not parsed.hostname or parsed.username or
            parsed.password or parsed.query or parsed.fragment or parsed.path not in ('', '/')):
        raise ValueError('generation status target must be an HTTPS origin')
    return urlunsplit(('https', parsed.netloc, '', '', ''))


def _bounded_body(stream):
    body = stream.read(MAX_RESPONSE_BYTES + 1)
    if len(body) > MAX_RESPONSE_BYTES:
        raise GenerationStatusError('generation status response exceeded the bounded response size')
    return body


def read_generation_status(target, project_id, generation_id, bearer_token,
                           timeout_seconds, ca_cert=None, proxy=None):
    """Perform exactly one authenticated GET and return status/body/type."""
    origin = _target_origin(target)
    project_id = str(project_id or '').strip()
    generation_id = str(generation_id or '').strip()
    bearer_token = str(bearer_token or '').strip()
    if not project_id or not generation_id or not bearer_token:
        raise ValueError('generation status project, generation, and bearer token are required')
    if timeout_seconds <= 0:
        raise ValueError('generation status request timeout must be positive')
    path = '/api/v1/projects/{}/delivery/generations/{}'.format(
        quote(project_id, safe=':'), quote(generation_id, safe=''))
    request = Request(origin + path, method='GET', headers={
        'Accept': 'application/json',
        'Authorization': 'Bearer ' + bearer_token,
    })
    handlers = [_NoRedirect()]
    handlers.append(ProxyHandler({'https': loopback_proxy(proxy)}) if proxy else ProxyHandler())
    if ca_cert:
        ca_path = Path(ca_cert)
        if not ca_path.is_file():
            raise ValueError('generation status CA certificate must be an existing file')
        handlers.append(HTTPSHandler(context=ssl.create_default_context(cafile=str(ca_path))))
    opener = build_opener(*handlers)
    try:
        with closing(opener.open(request, timeout=timeout_seconds)) as response:
            return response.getcode(), _bounded_body(response), response.headers.get('Content-Type', '')
    except HTTPError as response:
        with closing(response):
            return response.code, _bounded_body(response), response.headers.get('Content-Type', '')


def _decode_generation_json(body, description):
    try:
        value = json.loads(body)
    except (TypeError, ValueError, UnicodeDecodeError) as error:
        raise GenerationStatusError('{} did not return valid JSON'.format(description)) from error
    if not isinstance(value, dict):
        raise GenerationStatusError('{} JSON response was not an object'.format(description))
    return value


def _sanitized_body(body, bearer_token):
    try:
        value = json.loads(body)
    except (TypeError, ValueError, UnicodeDecodeError):
        text = body.decode('utf-8', errors='replace') if isinstance(body, bytes) else str(body)
        text = re.sub(r'(?i)(token|secret|password|authorization)(\s*[:=]\s*)[^\s,}\]]+',
                      r'\1\2[redacted]', text)
    else:
        def clean(item, key=''):
            if re.search(r'(?i)token|secret|password|authorization|credential', key):
                return '[redacted]'
            if isinstance(item, dict):
                return {name: clean(value, name) for name, value in item.items()}
            if isinstance(item, list):
                return [clean(value) for value in item]
            if isinstance(item, str):
                return item.replace(bearer_token, '[redacted]') if bearer_token else item
            return item
        text = json.dumps(clean(value), sort_keys=True, separators=(',', ':'))
    if bearer_token:
        text = text.replace(bearer_token, '[redacted]')
    if len(text) > 2048:
        text = text[:2048] + '…'
    return text


def _retry_diagnostic(http_status, content_type, body, bearer_token):
    return 'HTTP {} Content-Type={!r} body={}'.format(
        http_status, str(content_type)[:160], _sanitized_body(body, bearer_token))


def _sleep_before_generation_retry(deadline, poll_interval_seconds, monotonic, sleep):
    remaining = deadline - monotonic()
    if remaining <= 0:
        raise GenerationStatusError('delivery generation did not become active before the bounded deadline')
    sleep(min(poll_interval_seconds, remaining))


def wait_for_generation(target, project_id, generation_id, candidate_id,
                        bearer_token, timeout_seconds=DEFAULT_GENERATION_TIMEOUT_SECONDS,
                        poll_interval_seconds=DEFAULT_GENERATION_POLL_INTERVAL_SECONDS,
                        target_id=None, environment=None, ca_cert=None, proxy=None, read_status=read_generation_status,
                        monotonic=time.monotonic, sleep=time.sleep, diagnostic=None):
    """Read-only poll until the exact generation identity is active."""
    origin = _target_origin(target)
    project_id = str(project_id or '').strip()
    generation_id = str(generation_id or '').strip()
    candidate_id = str(candidate_id or '').strip()
    bearer_token = str(bearer_token or '').strip()
    if not project_id or not generation_id or not candidate_id or not bearer_token:
        raise ValueError('generation status project, generation, candidate, and bearer token are required')
    if timeout_seconds <= 0 or poll_interval_seconds <= 0:
        raise ValueError('generation status timeout and poll interval must be positive')
    if os.environ.get('DEMO_CLONE_ONLY') == '1':
        proxy = loopback_proxy(proxy or os.environ.get('DEMO_CLONE_PROXY', ''))
        if origin != CLONE_TARGET:
            raise ValueError('clone-only generation status must keep the canonical demo origin')
    if diagnostic is None:
        diagnostic = lambda message: print(message, file=sys.stderr)
    deadline = monotonic() + timeout_seconds
    while True:
        remaining = deadline - monotonic()
        if remaining <= 0:
            raise GenerationStatusError('delivery generation did not become active before the bounded deadline')
        try:
            http_status, body, content_type = read_status(
                origin, project_id, generation_id, bearer_token,
                min(10.0, remaining), ca_cert=ca_cert, proxy=proxy)
        except GenerationStatusError:
            raise
        except Exception as error:
            reason = getattr(error, 'reason', error)
            safe_reason = _sanitized_body(str(reason), bearer_token)
            raise GenerationStatusError('generation status GET failed: {}: {}'.format(
                type(error).__name__, safe_reason)) from error

        if http_status == 503:
            diagnostic('generation status read returned ' +
                       _retry_diagnostic(http_status, content_type, body, bearer_token))
            if (str(content_type).strip().lower() == 'text/plain' and
                    isinstance(body, bytes) and body.strip() == b'Service Unavailable'):
                _sleep_before_generation_retry(deadline, poll_interval_seconds, monotonic, sleep)
                continue
            try:
                problem = _decode_generation_json(body, 'generation status 503 response')
            except GenerationStatusError as error:
                raise GenerationStatusError('generation status 503 is not retryable; ' +
                                            _retry_diagnostic(http_status, content_type, body, bearer_token)) from error
            if problem.get('code') != TRANSIENT_READ_CODE:
                raise GenerationStatusError('generation status 503 is not retryable; ' +
                                            _retry_diagnostic(http_status, content_type, body, bearer_token))
            _sleep_before_generation_retry(deadline, poll_interval_seconds, monotonic, sleep)
            continue
        if http_status != 200:
            raise GenerationStatusError('generation status GET failed; ' +
                                        _retry_diagnostic(http_status, content_type, body, bearer_token))

        generation = _decode_generation_json(body, 'generation status')
        expected = {'id': generation_id, 'projectId': project_id, 'candidateId': candidate_id}
        if target_id:
            expected['targetId'] = target_id
        if environment:
            expected['environment'] = environment
        mismatches = [field for field, value in expected.items() if generation.get(field) != value]
        if mismatches:
            raise GenerationStatusError('generation status identity mismatch: {}'.format(', '.join(mismatches)))
        status = generation.get('status')
        if status == 'active':
            return generation
        if status != 'prepared':
            raise GenerationStatusError('generation status is {!r}, expected prepared or active'.format(status))
        _sleep_before_generation_retry(deadline, poll_interval_seconds, monotonic, sleep)


def permission_profile(profile):
    if profile not in PROFILES:
        raise ValueError('DEMO_PERMISSION_PROFILE must name a supported, trusted profile')
    return PROFILES[profile]


def scope_for(profile, role):
    if role not in ('publisher', 'release', 'inspection'):
        raise ValueError('demo workload role must be publisher, release, or inspection')
    return permission_profile(profile)[role]


def loopback_proxy(value):
    if not isinstance(value, str) or not re.fullmatch(r'http://127\.0\.0\.1:[1-9][0-9]{0,4}', value):
        raise ValueError('DEMO_CLONE_PROXY must be an HTTP proxy on 127.0.0.1')
    port = int(value.rsplit(':', 1)[1])
    if not 1024 <= port <= 65535:
        raise ValueError('DEMO_CLONE_PROXY port is outside the permitted range')
    return value


def clone_only_environment(base, proxy):
    """Route common clients through one loopback proxy with no bypass list."""
    proxy = loopback_proxy(proxy)
    result = {key: value for key, value in base.items()
              if key not in PROXY_NAMES and key not in BYPASS_NAMES}
    for key in PROXY_NAMES:
        result[key] = proxy
    # An empty value overrides inherited bypasses in curl, urllib and Go.
    for key in BYPASS_NAMES:
        result[key] = ''
    result.update(DEMO_CLONE_ONLY='1', DEMO_CLONE_PROXY=proxy,
                  DEMO_TARGET=CLONE_TARGET)
    return result


def validate_client_environment(env=None):
    env = os.environ if env is None else env
    profile = env.get('DEMO_PERMISSION_PROFILE', '')
    permission_profile(profile)
    if env.get('DEMO_CLONE_ONLY') == '1':
        proxy = loopback_proxy(env.get('DEMO_CLONE_PROXY'))
        if env.get('DEMO_TARGET') != CLONE_TARGET:
            raise ValueError('Clone-only demo clients must keep the canonical demo origin')
        for key in PROXY_NAMES:
            if env.get(key) != proxy:
                raise ValueError('Clone-only clients must set every supported proxy variable')
        for key in BYPASS_NAMES:
            if env.get(key) != '':
                raise ValueError('Clone-only clients must clear every proxy bypass variable')
    return profile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--profile', default=os.environ.get('DEMO_PERMISSION_PROFILE'))
    parser.add_argument('--role', choices=('publisher', 'release', 'inspection'))
    parser.add_argument('--validate-environment', action='store_true')
    parser.add_argument('--wait-generation', action='store_true')
    parser.add_argument('--target', default=os.environ.get('DEMO_TARGET'))
    parser.add_argument('--project')
    parser.add_argument('--generation')
    parser.add_argument('--candidate')
    parser.add_argument('--target-id')
    parser.add_argument('--environment')
    parser.add_argument('--token-env', default='DEMO_GENERATION_TOKEN')
    parser.add_argument('--timeout', type=float, default=DEFAULT_GENERATION_TIMEOUT_SECONDS)
    parser.add_argument('--poll-interval', type=float, default=DEFAULT_GENERATION_POLL_INTERVAL_SECONDS)
    parser.add_argument('--ca-cert', default=os.environ.get('DEMO_GENERATION_CA_CERT'))
    parser.add_argument('--proxy', default=os.environ.get('DEMO_GENERATION_PROXY'))
    args = parser.parse_args()
    try:
        if args.validate_environment:
            validate_client_environment()
        if args.role:
            print(scope_for(args.profile, args.role))
        if args.wait_generation:
            token = os.environ.get(args.token_env, '')
            if not args.target:
                raise ValueError('DEMO_TARGET or --target is required')
            if not token:
                raise ValueError('{} must contain the existing workload bearer token'.format(args.token_env))
            generation = wait_for_generation(
                args.target, args.project, args.generation, args.candidate, token,
                timeout_seconds=args.timeout, poll_interval_seconds=args.poll_interval,
                target_id=args.target_id, environment=args.environment,
                ca_cert=args.ca_cert, proxy=args.proxy)
            print(json.dumps({
                'id': generation['id'], 'projectId': generation['projectId'],
                'candidateId': generation['candidateId'], 'status': generation['status'],
                **({'targetId': generation['targetId']} if args.target_id else {}),
                **({'environment': generation['environment']} if args.environment else {}),
            }, sort_keys=True))
    except ValueError as error:
        print(str(error), file=sys.stderr)
        return 64
    except GenerationStatusError as error:
        print(str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
