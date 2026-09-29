#!/usr/bin/env python3
"""Resolve and reconcile the verified runtime through GitHub deployments."""
import json
import os
from pathlib import Path
import re
import sys

from demo_release_contract import read_contract

BASE = 'repos/flidai/leapview'
TASK = 'demo-compose-runtime'
ENVIRONMENT = 'leapview-demo-runtime'
BINDING_FILENAME = 'demo-runtime-binding.json'
OUTCOME_FILENAME = 'demo-host-outcome.json'
ATTEMPT_KIND = 'leapview-runtime-attempt-v1'
RECOVERY_KIND = 'leapview-runtime-recovery-v1'
REVISION_RE = re.compile(r'^[0-9a-f]{40}$')
IMAGE_RE = re.compile(r'^ghcr\.io/flidai/leapview@sha256:([0-9a-f]{64})$')
OPERATION_RE = re.compile(r'^sha256:[0-9a-f]{64}$')
RUN_ID_RE = re.compile(r'^[0-9]+$')
OPERATIONS = {'deploy', 'upgrade', 'recover'}


def api(path, body=None):
    import subprocess

    args = ['gh', 'api', BASE + '/' + path]
    if body is not None:
        args += ['--method', 'POST', '--input', '-']
    result = subprocess.check_output(
        args, input=None if body is None else json.dumps(body).encode())
    return json.loads(result)


def _identity(value, label):
    if not isinstance(value, dict):
        raise ValueError(f'{label} identity is required')
    image = value.get('image')
    revision = value.get('revision')
    if not isinstance(image, str) or not IMAGE_RE.fullmatch(image):
        raise ValueError(f'{label} image must be an immutable LeapView digest')
    if not isinstance(revision, str) or not REVISION_RE.fullmatch(revision):
        raise ValueError(f'{label} revision must be a full Git commit identity')
    return {'image': image, 'revision': revision}


def _payload(record):
    value = record.get('payload')
    if isinstance(value, str):
        value = json.loads(value)
    if not isinstance(value, dict):
        return {}
    return value


def _read_json(path, label):
    try:
        data = Path(path).read_text()
        value = json.loads(data)
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f'{label} is unavailable or invalid JSON') from exc
    if not isinstance(value, dict):
        raise ValueError(f'{label} must be a JSON object')
    return value


def _validate_binding(value):
    if not isinstance(value, dict):
        raise ValueError('Runtime binding must be a JSON object')
    if value.get('version') != 1:
        raise ValueError('Unknown runtime binding version')
    operation = value.get('operation')
    if operation not in OPERATIONS:
        raise ValueError('Runtime binding has an unknown operation')
    operation_id = value.get('operationId')
    if not isinstance(operation_id, str) or not OPERATION_RE.fullmatch(operation_id):
        raise ValueError('Runtime binding operation digest is invalid')
    candidate = _identity(value.get('candidate'), 'Candidate')
    predecessor = _identity(value.get('predecessor'), 'Predecessor')
    run_id = value.get('workflowRunId')
    attempt = value.get('workflowAttempt')
    if not isinstance(run_id, str) or not RUN_ID_RE.fullmatch(run_id):
        raise ValueError('Runtime binding workflow run ID is invalid')
    if not isinstance(attempt, str) or not RUN_ID_RE.fullmatch(attempt):
        raise ValueError('Runtime binding workflow attempt is invalid')
    return {
        'version': 1,
        'operation': operation,
        'operationId': operation_id,
        'candidate': candidate,
        'predecessor': predecessor,
        'workflowRunId': run_id,
        'workflowAttempt': attempt,
    }


def _binding():
    runner_temp = os.environ.get('RUNNER_TEMP')
    if not runner_temp:
        raise ValueError('RUNNER_TEMP is required for runtime deployment start')
    value = _read_json(Path(runner_temp) / BINDING_FILENAME, 'Runtime binding')
    return _validate_binding(value)


def _binding_from_record(record):
    payload = _payload(record)
    if payload.get('recordType') != ATTEMPT_KIND:
        raise ValueError('Runtime deployment record has no supported outcome contract')
    binding = {key: payload.get(key) for key in (
        'version', 'operation', 'operationId', 'candidate', 'predecessor',
        'workflowRunId', 'workflowAttempt')}
    return _validate_binding(binding)


def binding_for_deployment(deployment_id):
    """Load the immutable attempt binding without consulting workflow inputs."""
    if not isinstance(deployment_id, str) or not RUN_ID_RE.fullmatch(deployment_id):
        raise ValueError('Deployment ID must be numeric')
    deployment = api(f'deployments/{deployment_id}')
    binding = _binding_from_record(deployment)
    _validate_attempt(deployment, binding)
    return binding


def _record_payload(binding):
    return {
        'recordType': ATTEMPT_KIND,
        **binding,
    }


def _list_runtime_deployments():
    records = api(f'deployments?environment={ENVIRONMENT}&task={TASK}&per_page=100')
    if not isinstance(records, list):
        raise RuntimeError('GitHub returned an invalid runtime deployment list')
    return records


def _same_binding(record, binding):
    payload = _payload(record)
    return (
        payload.get('recordType') == ATTEMPT_KIND
        and payload.get('workflowRunId') == binding['workflowRunId']
        and payload.get('workflowAttempt') == binding['workflowAttempt']
    )


def _validate_attempt(record, binding):
    payload = _payload(record)
    if record.get('task') != TASK or record.get('environment') != ENVIRONMENT:
        raise ValueError('Runtime deployment record targets a different task or environment')
    if payload.get('recordType') != ATTEMPT_KIND:
        raise ValueError('Runtime deployment record has no supported outcome contract')
    stored = {key: payload.get(key) for key in (
        'version', 'operation', 'operationId', 'candidate', 'predecessor',
        'workflowRunId', 'workflowAttempt')}
    if stored != binding:
        raise ValueError('Runtime binding differs from the immutable deployment record')
    if record.get('sha') != binding['candidate']['revision']:
        raise ValueError('Runtime deployment SHA differs from its admitted candidate')


def _new_deployment(ref, payload):
    record = api('deployments', dict(
        ref=ref,
        task=TASK,
        environment=ENVIRONMENT,
        auto_merge=False,
        required_contexts=[],
        production_environment=True,
        payload=payload,
    ))
    if not isinstance(record, dict) or not record.get('id'):
        raise RuntimeError('GitHub did not return a runtime deployment ID')
    return record


def start():
    binding = _binding()
    expected_revision = os.environ.get('SOURCE_REVISION', '')
    expected_image = os.environ.get('DEMO_IMAGE', '')
    if (expected_revision != binding['candidate']['revision']
            or expected_image != binding['candidate']['image']):
        raise ValueError('Runtime binding differs from the admitted candidate')

    matches = [record for record in _list_runtime_deployments()
               if _same_binding(record, binding)]
    if len(matches) > 1:
        raise RuntimeError('Duplicate GitHub runtime records exist for this operation attempt')
    if matches:
        record = matches[0]
        _validate_attempt(record, binding)
    else:
        record = _new_deployment(binding['candidate']['revision'], _record_payload(binding))
        _validate_attempt(record, binding)

    deployment_id = str(record['id'])
    _start_status(deployment_id, binding['operationId'])
    output_path = os.environ.get('GITHUB_OUTPUT')
    if not output_path:
        raise ValueError('GITHUB_OUTPUT is required')
    with open(output_path, 'a') as output:
        output.write(f'id={deployment_id}\n')
        output.write(f'operation_id={binding["operationId"]}\n')


def _statuses(deployment_id):
    statuses = api(f'deployments/{deployment_id}/statuses?per_page=100')
    if not isinstance(statuses, list):
        raise RuntimeError('GitHub returned invalid runtime deployment statuses')
    return statuses


def _status_description(result, operation_id):
    return f'leapview-runtime:v1:{result}:{operation_id}'


def _start_status(deployment_id, operation_id):
    statuses = _statuses(deployment_id)
    description = _status_description('in_progress', operation_id)
    if statuses:
        if statuses[0].get('state') == 'in_progress' and statuses[0].get('description') == description:
            return statuses[0]
        # A retry after terminal reconciliation must leave the recorded result intact.
        return statuses[0]
    run_id = os.environ.get('GITHUB_RUN_ID', '')
    status = dict(
        state='in_progress',
        environment_url='https://demo.leapview.dev',
        auto_inactive=False,
        description=description,
    )
    if run_id and RUN_ID_RE.fullmatch(run_id):
        status['log_url'] = f'https://github.com/flidai/leapview/actions/runs/{run_id}'
    return api(f'deployments/{deployment_id}/statuses', status)


def _ensure_status(deployment_id, state, result, operation_id):
    description = _status_description(result, operation_id)
    statuses = _statuses(deployment_id)
    if statuses and statuses[0].get('state') == state and statuses[0].get('description') == description:
        return statuses[0]
    run_id = os.environ.get('GITHUB_RUN_ID', '')
    status = dict(
        state=state,
        environment_url='https://demo.leapview.dev',
        auto_inactive=False,
        description=description,
    )
    if run_id and RUN_ID_RE.fullmatch(run_id):
        status['log_url'] = f'https://github.com/flidai/leapview/actions/runs/{run_id}'
    return api(f'deployments/{deployment_id}/statuses', status)


def _outcome(path, deployment_id, binding):
    value = _read_json(path, 'Host outcome')
    if value.get('version') != 1:
        raise ValueError('Unknown host outcome version')
    if value.get('deploymentId') != str(deployment_id):
        raise ValueError('Host outcome belongs to a different GitHub deployment')
    if value.get('operation') != binding['operation']:
        raise ValueError('Host outcome operation differs from the admitted binding')
    if value.get('operationId') != binding['operationId']:
        raise ValueError('Host outcome digest differs from the admitted operation')
    if _identity(value.get('candidate'), 'Outcome candidate') != binding['candidate']:
        raise ValueError('Host outcome candidate differs from the admitted image')
    if _identity(value.get('predecessor'), 'Outcome predecessor') != binding['predecessor']:
        raise ValueError('Host outcome predecessor differs from the admitted predecessor')
    result = value.get('result')
    if result not in {'committed', 'recovered', 'unresolved'}:
        raise ValueError('Host outcome has an unknown result')
    return value


def _verified_result(outcome, binding):
    """Return committed/recovered only when all independently checked IDs match."""
    result = outcome['result']
    if result == 'unresolved':
        return 'unresolved'
    if (outcome.get('hostRuntimeVerified') is not True
            or outcome.get('containerImageVerified') is not True
            or outcome.get('publicIdentityVerified') is not True):
        return 'unresolved'
    if (binding['operation'] in {'upgrade', 'recover'}
            and outcome.get('hostJournalVerified') is not True):
        return 'unresolved'
    candidate_schema = outcome.get('candidateSchema')
    predecessor_schema = outcome.get('predecessorSchema')
    observed_schema = outcome.get('observedSchema')
    if (type(candidate_schema) is not int or candidate_schema < 1
            or type(predecessor_schema) is not int or predecessor_schema < 1
            or type(observed_schema) is not int or observed_schema < 1
            or outcome.get('schemaVerified') is not True):
        return 'unresolved'
    observed = _identity(outcome.get('observed'), 'Observed runtime')
    if result == 'committed':
        if binding['operation'] in {'upgrade', 'recover'}:
            if outcome.get('journalState') not in {'committed', 'succeeded'}:
                return 'unresolved'
        elif outcome.get('journalState') != 'image-only-committed':
            return 'unresolved'
        if observed != binding['candidate']:
            return 'unresolved'
        if observed_schema != candidate_schema:
            return 'unresolved'
        return 'committed'
    if result == 'recovered':
        if binding['operation'] in {'upgrade', 'recover'}:
            if outcome.get('journalState') != 'recovered':
                return 'unresolved'
        elif outcome.get('journalState') != 'image-only-recovered':
            return 'unresolved'
        if observed != binding['predecessor']:
            return 'unresolved'
        if observed_schema != predecessor_schema:
            return 'unresolved'
        return 'recovered'
    return 'unresolved'


def _recovery_payload(binding, candidate_deployment_id):
    predecessor = binding['predecessor']
    return {
        'recordType': RECOVERY_KIND,
        'outcomeVersion': 1,
        'operationId': binding['operationId'],
        'operation': binding['operation'],
        'image': predecessor['image'],
        'revision': predecessor['revision'],
        'candidate': binding['candidate'],
        'predecessor': predecessor,
        'recoveredFrom': str(candidate_deployment_id),
        'workflowRunId': binding['workflowRunId'],
    }


def _ensure_recovery_record(binding, candidate_deployment_id):
    wanted = _recovery_payload(binding, candidate_deployment_id)
    matches = []
    for record in _list_runtime_deployments():
        payload = _payload(record)
        if (payload.get('recordType') == RECOVERY_KIND
                and payload.get('operationId') == binding['operationId']
                and payload.get('recoveredFrom') == str(candidate_deployment_id)):
            matches.append(record)
    if len(matches) > 1:
        raise RuntimeError('Duplicate recovered runtime records exist for this operation')
    if matches:
        record = matches[0]
        if (record.get('task') != TASK or record.get('environment') != ENVIRONMENT
                or record.get('sha') != binding['predecessor']['revision']
                or _payload(record) != wanted):
            raise RuntimeError('Existing recovered runtime record conflicts with verified outcome')
    else:
        record = _new_deployment(binding['predecessor']['revision'], wanted)
    recovered_id = str(record['id'])
    _ensure_status(recovered_id, 'success', 'recovered', binding['operationId'])
    return recovered_id


def _summarize_outcome(binding, outcome, verified):
    path = os.environ.get('GITHUB_STEP_SUMMARY')
    if not path:
        return
    running = binding['candidate'] if verified == 'committed' else binding['predecessor'] if verified == 'recovered' else None
    next_step = {
        'committed': 'Runtime verified; content publication must pass before the release is complete.',
        'recovered': 'The candidate was not deployed. Correct the failed phase and prepare again; the verified predecessor remains selected.',
        'unresolved': 'Publication is blocked. Inspect and reconcile this operation before another deployment.',
    }[verified]
    schema = lambda value: str(value) if type(value) is int and value > 0 else 'unknown'
    # Use only validated identities and bounded status fields. Remote error
    # text and arbitrary receipt fields may contain private diagnostics.
    with open(path, 'a') as output:
        output.write('\n### Verified host outcome\n\n')
        output.write(f'- Result: **{verified}**\n')
        output.write(f'- Operation: `{binding["operationId"]}`\n')
        output.write(f'- Candidate: `{binding["candidate"]["revision"]}` / `{binding["candidate"]["image"]}`\n')
        output.write(f'- Requested schema: {schema(outcome.get("predecessorSchema"))} → {schema(outcome.get("candidateSchema"))}\n')
        if running:
            output.write(f'- Verified running source/image: `{running["revision"]}` / `{running["image"]}`\n')
        output.write(f'- Next: {next_step}\n')
        output.write('\nGitHub runtime-record reconciliation follows this host observation.\n')


def reconcile():
    deployment_id = os.environ.get('DEPLOYMENT_ID', '')
    if not RUN_ID_RE.fullmatch(deployment_id):
        raise ValueError('DEPLOYMENT_ID must be a numeric GitHub deployment ID')
    binding = binding_for_deployment(deployment_id)
    for name, expected in (
        ('SOURCE_REVISION', binding['candidate']['revision']),
        ('DEMO_IMAGE', binding['candidate']['image']),
    ):
        value = os.environ.get(name)
        if value and value != expected:
            raise ValueError(f'{name} differs from the immutable runtime deployment record')

    runner_temp = os.environ.get('RUNNER_TEMP')
    if not runner_temp:
        raise ValueError('RUNNER_TEMP is required for host outcome reconciliation')
    outcome_path = os.environ.get('HOST_OUTCOME_FILE', str(Path(runner_temp) / OUTCOME_FILENAME))
    try:
        outcome = _outcome(outcome_path, deployment_id, binding)
        verified = _verified_result(outcome, binding)
    except (OSError, ValueError, json.JSONDecodeError):
        _summarize_outcome(binding, {}, 'unresolved')
        _ensure_status(deployment_id, 'error', 'unresolved', binding['operationId'])
        raise

    _summarize_outcome(binding, outcome, verified)
    if verified == 'committed':
        _ensure_status(deployment_id, 'success', 'committed', binding['operationId'])
        recovered_id = ''
        revision = binding['candidate']['revision']
        image = binding['candidate']['image']
    elif verified == 'recovered':
        _ensure_status(deployment_id, 'failure', 'recovered', binding['operationId'])
        recovered_id = _ensure_recovery_record(binding, deployment_id)
        revision = binding['predecessor']['revision']
        image = binding['predecessor']['image']
    else:
        _ensure_status(deployment_id, 'error', 'unresolved', binding['operationId'])
        raise RuntimeError('Host outcome is unresolved; runtime pin remains blocked')

    permission_profile = read_contract(revision)['permissionProfile']

    output_path = os.environ.get('GITHUB_OUTPUT')
    if not output_path:
        raise ValueError('GITHUB_OUTPUT is required')
    with open(output_path, 'a') as output:
        output.write(f'running_revision={revision}\n')
        output.write(f'running_image={image}\n')
        output.write(f'permission_profile={permission_profile}\n')
        output.write(f'recovered_deployment_id={recovered_id}\n')


def _resolved_identity(record, statuses):
    if not statuses or statuses[0].get('state') != 'success':
        raise RuntimeError('Latest runtime deployment is not successful; reconcile before publication')
    payload = _payload(record)
    revision = record.get('sha', '')
    if not isinstance(revision, str) or not REVISION_RE.fullmatch(revision):
        raise ValueError('Latest runtime deployment has no full source revision')

    if payload.get('recordType') == RECOVERY_KIND:
        operation_id = payload.get('operationId', '')
        if not isinstance(operation_id, str) or not OPERATION_RE.fullmatch(operation_id):
            raise ValueError('Recovered runtime record has an invalid operation digest')
        if (payload.get('revision') != revision
                or payload.get('image') != payload.get('predecessor', {}).get('image')
                or payload.get('revision') != payload.get('predecessor', {}).get('revision')):
            raise ValueError('Recovered runtime record identity is inconsistent')
        expected = _status_description('recovered', operation_id)
        if statuses[0].get('description') != expected:
            raise RuntimeError('Recovered runtime record lacks a matching verified outcome status')
        identity = _identity({'image': payload.get('image'), 'revision': revision}, 'Recovered runtime')
        return identity

    if payload.get('recordType') == ATTEMPT_KIND:
        operation_id = payload.get('operationId', '')
        if not isinstance(operation_id, str) or not OPERATION_RE.fullmatch(operation_id):
            raise ValueError('Runtime record has an invalid operation digest')
        candidate = _identity(payload.get('candidate'), 'Recorded candidate')
        if candidate['revision'] != revision:
            raise ValueError('Runtime record candidate SHA differs from its deployment SHA')
        if statuses[0].get('description') != _status_description('committed', operation_id):
            raise RuntimeError('Runtime deployment lacks a matching verified outcome status')
        return candidate

    # Older verified deployments predate the outcome-record contract.
    image = payload.get('image')
    if image is None:
        return {'image': '', 'revision': revision}
    return _identity({'image': image, 'revision': revision}, 'Legacy runtime')


def resolve():
    records = api(f'deployments?environment={ENVIRONMENT}&task={TASK}&per_page=1')
    if not isinstance(records, list):
        raise RuntimeError('GitHub returned an invalid runtime deployment list')
    if records:
        record = records[0]
        statuses = _statuses(str(record['id']))
        identity = _resolved_identity(record, statuses)
        revision, image = identity['revision'], identity['image']
    else:
        revision = os.environ.get('DEMO_RUNTIME_REVISION', '')
        image = os.environ.get('DEMO_RUNTIME_IMAGE', '')
    if not isinstance(revision, str) or not REVISION_RE.fullmatch(revision):
        raise ValueError('No verified runtime pin')
    if image and not IMAGE_RE.fullmatch(image):
        raise ValueError('Runtime pin image is not an immutable LeapView digest')
    permission_profile = read_contract(revision)['permissionProfile']
    output_path = os.environ.get('GITHUB_OUTPUT')
    if not output_path:
        raise ValueError('GITHUB_OUTPUT is required')
    with open(output_path, 'a') as output:
        output.write(f'revision={revision}\n')
        output.write(f'permission_profile={permission_profile}\n')
        if image:
            output.write(f'image={image}\n')
        output.write('publish=true\n')


def main():
    if len(sys.argv) not in (2, 3):
        raise ValueError('Usage: demo_runtime_record.py resolve|start|reconcile|binding [DEPLOYMENT_ID]')
    action = sys.argv[1]
    if action == 'resolve':
        return resolve()
    if action == 'start':
        return start()
    if action == 'reconcile':
        return reconcile()
    if action == 'binding':
        deployment_id = sys.argv[2] if len(sys.argv) == 3 else os.environ.get('DEPLOYMENT_ID', '')
        print(json.dumps(binding_for_deployment(deployment_id), sort_keys=True))
        return
    if action == 'success':
        raise ValueError('Runtime success requires a verified host outcome; use reconcile')
    raise ValueError(f'Unknown runtime record action {action!r}')


if __name__ == '__main__':
    main()
