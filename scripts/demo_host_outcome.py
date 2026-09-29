#!/usr/bin/env python3
"""Collect and verify a read-only, digest-bound demo host outcome."""
import datetime
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import tempfile

from demo_runtime_record import OUTCOME_FILENAME, _validate_binding

RUN_ID_RE = re.compile(r'^[0-9]+$')
REVISION_RE = re.compile(r'^[0-9a-f]{40}$')
IMAGE_RE = re.compile(r'^ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}$')
OPERATION_RE = re.compile(r'^sha256:[0-9a-f]{64}$')
INSTANCE_ID_RE = re.compile(r'^(lvinst_[A-Za-z0-9_-]{32}|instance_[0-9a-f]{32})$')
TERMINAL_COMMIT = {'committed', 'succeeded'}
INSTALLATION_VERSION = 'leapview-compose-installation-v1'


def _schema(expected_schema, revision):
    value = expected_schema(revision)
    if type(value) is not int or value < 1:
        raise ValueError('Source schema resolver returned an invalid schema')
    return value


def _valid_utc_timestamp(value):
    if not isinstance(value, str) or not value.endswith('Z'):
        return False
    try:
        parsed = datetime.datetime.fromisoformat(value[:-1]+'+00:00')
    except ValueError:
        return False
    return parsed.utcoffset() == datetime.timedelta(0)


def _verify_installation_evidence(value, runtime, binding, schema):
    fields = {
        'version', 'host', 'installationRoot', 'hostTargetId', 'instanceId', 'image',
        'revision', 'schema', 'permissionProfile', 'qualificationRunId',
        'qualificationAttempt', 'validatedAt',
    }
    qualification_fields = ('qualificationRunId', 'qualificationAttempt', 'permissionProfile')
    if set(binding).issuperset(qualification_fields) is False:
        raise ValueError('Installation evidence requires an admitted qualification binding')
    if not isinstance(value, dict) or set(value) != fields:
        raise ValueError('Installation evidence has an unsupported shape')
    observed = {'image': runtime.get('image'), 'revision': runtime.get('revision')}
    if (value.get('version') != INSTALLATION_VERSION
            or value.get('host') != 'app-leapview-demo-02'
            or value.get('installationRoot') != '/opt/leapview'
            or not isinstance(runtime.get('markerTargetId'), str)
            or value.get('hostTargetId') != runtime.get('markerTargetId')
            or not isinstance(runtime.get('instanceId'), str)
            or not INSTANCE_ID_RE.fullmatch(runtime['instanceId'])
            or value.get('instanceId') != runtime.get('instanceId')
            or value.get('image') != observed['image']
            or value.get('revision') != observed['revision']
            or type(value.get('schema')) is not int or value.get('schema') != schema
            or value.get('qualificationRunId') != binding['qualificationRunId']
            or value.get('qualificationAttempt') != binding['qualificationAttempt']
            or value.get('permissionProfile') != binding['permissionProfile']
            or not _valid_utc_timestamp(value.get('validatedAt'))):
        raise ValueError('Installation evidence differs from its qualified live identity')


def _remote_snapshot(ssh, remote_runtime, operation, operation_id):
    remote_command = 'python3 {} outcome {} {}'.format(
        shlex.quote(str(remote_runtime)), shlex.quote(operation), shlex.quote(operation_id))
    raw = subprocess.check_output(
        [*ssh, remote_command], stderr=subprocess.DEVNULL, timeout=180)
    try:
        value = json.loads(raw)
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        raise ValueError('Host outcome helper returned invalid JSON') from exc
    if not isinstance(value, dict):
        raise ValueError('Host outcome helper returned an invalid report')
    return value


def _verify_snapshot(snapshot, binding, candidate_schema, predecessor_schema):
    if snapshot.get('version') != 1:
        raise ValueError('Unknown host snapshot version')
    if snapshot.get('operation') != binding['operation']:
        raise ValueError('Host snapshot operation differs from the runtime binding')
    if snapshot.get('operationId') != binding['operationId']:
        raise ValueError('Host snapshot digest differs from the runtime binding')

    request = snapshot.get('request')
    journal = snapshot.get('journal')
    phase = snapshot.get('journalState')
    if binding['operation'] in {'upgrade', 'recover'}:
        if not isinstance(request, dict) or not isinstance(journal, dict):
            raise ValueError('Native request and journal evidence are required')
        identity = journal.get('identity')
        if not isinstance(identity, dict):
            raise ValueError('Native journal identity is missing')
        if (identity.get('artifactAdmissionDigest') != binding['operationId']
                or identity.get('candidate') != binding['candidate']['image']
                or identity.get('predecessor') != binding['predecessor']['image']):
            raise ValueError('Native journal is not bound to this exact operation')
        if (request.get('candidateImage') != binding['candidate']['image']
                or request.get('candidateRevision') != binding['candidate']['revision']
                or request.get('predecessorImage') != binding['predecessor']['image']
                or request.get('predecessorRevision') != binding['predecessor']['revision']):
            raise ValueError('Persisted request differs from the runtime binding')
        if request.get('currentSchema') != predecessor_schema:
            raise ValueError('Persisted predecessor schema differs from the admitted source')
        if request.get('candidateSchema') != candidate_schema:
            raise ValueError('Persisted candidate schema differs from the admitted source')
        if identity.get('target') != request.get('profileID'):
            raise ValueError('Native request profile differs from the journal target')
    else:
        if request is not None or journal is not None or phase is not None:
            raise ValueError('Image-only operation unexpectedly claims a native journal')
        if candidate_schema != predecessor_schema:
            raise ValueError('Image-only deployment cannot change the database schema')

    runtime = snapshot.get('runtime')
    if not isinstance(runtime, dict):
        raise ValueError('Current host runtime identity is missing')
    image = runtime.get('image')
    revision = runtime.get('revision')
    schema = runtime.get('schema')
    if not isinstance(image, str) or not IMAGE_RE.fullmatch(image):
        raise ValueError('Current host image is not an immutable LeapView digest')
    if not isinstance(revision, str) or not REVISION_RE.fullmatch(revision):
        raise ValueError('Current host source revision is invalid')
    if type(schema) is not int or schema < 1:
        raise ValueError('Current host schema is invalid')

    image_id = runtime.get('containerImageID')
    repository_digests = runtime.get('repositoryDigests')
    if not isinstance(image_id, str) or not image_id.startswith('sha256:'):
        raise ValueError('Container image ID is missing')
    if not isinstance(repository_digests, list) or image not in repository_digests:
        raise ValueError('Container content does not match the immutable image digest')
    if runtime.get('descriptorImage') != image or runtime.get('markerImage') != image:
        raise ValueError('Host deployment descriptor differs from the running container')

    evidence_type = runtime.get('evidenceType')
    if evidence_type == 'deployment':
        if (runtime.get('receiptImage') != image
                or runtime.get('receiptRevision') != revision
                or runtime.get('installationEvidence') is not None):
            raise ValueError('Host deployment receipt differs from the running container')
    elif evidence_type == 'installation':
        if (binding['operation'] != 'deploy'
                or binding['candidate'] != binding['predecessor']):
            raise ValueError('Installation evidence is only valid for a same-image deploy')
        _verify_installation_evidence(runtime.get('installationEvidence'), runtime, binding, schema)
    else:
        raise ValueError('Host runtime evidence type is unknown')

    candidate = binding['candidate']
    predecessor = binding['predecessor']
    observed = {'image': image, 'revision': revision}
    is_candidate = observed == candidate and schema == candidate_schema
    is_predecessor = observed == predecessor and schema == predecessor_schema
    if not (is_candidate or is_predecessor):
        return None, observed, schema

    if binding['operation'] == 'deploy':
        if candidate == predecessor and is_candidate:
            # No-op deploy records use a run-bound synthetic digest and do not
            # rewrite the older deployment receipt on the host.
            return 'committed', observed, schema
        if (is_candidate and evidence_type == 'deployment'
                and runtime.get('receiptPreviousImage') == predecessor['image']):
            return 'committed', observed, schema
        if is_predecessor:
            return 'recovered', observed, schema
        return None, observed, schema
    if phase in TERMINAL_COMMIT and is_candidate:
        return 'committed', observed, schema
    if phase == 'recovered' and is_predecessor:
        return 'recovered', observed, schema
    return None, observed, schema


def _write_atomic(path, value):
    destination = Path(path)
    if not destination.parent.is_dir():
        raise ValueError('Host outcome destination directory is unavailable')
    payload = (json.dumps(value, sort_keys=True, separators=(',', ':')) + '\n').encode()
    fd, temporary = tempfile.mkstemp(prefix='.demo-host-outcome-', dir=destination.parent)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(payload)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, destination)
        directory_fd = os.open(destination.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    except BaseException:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise


def collect(ssh, remote_runtime, binding, deployment_id, verify_public_revision,
            expected_schema, output_path=None):
    """Write a verified outcome receipt and return it; uncertainty stays unresolved.

    `verify_public_revision` must authenticate against the demo origin and raise
    unless its build revision equals the requested source. `expected_schema`
    resolves source schema from the already-admitted immutable revision.
    """
    binding = _validate_binding(binding)
    if not isinstance(deployment_id, str) or not RUN_ID_RE.fullmatch(deployment_id):
        raise ValueError('GitHub deployment ID must be numeric')
    if not callable(verify_public_revision) or not callable(expected_schema):
        raise ValueError('Authenticated public and source-schema verifiers are required')
    if output_path is None:
        runner_temp = os.environ.get('RUNNER_TEMP')
        if not runner_temp:
            raise ValueError('RUNNER_TEMP is required for the host outcome receipt')
        output_path = Path(runner_temp) / OUTCOME_FILENAME

    candidate_schema = None
    predecessor_schema = None
    snapshot = None
    observed = None
    observed_schema = None
    journal_state = 'unknown'
    failure = None
    result = 'unresolved'
    host_journal_verified = False
    host_runtime_verified = False
    container_image_verified = False
    public_identity_verified = False
    host_evidence_type = None
    deployment_receipt_verified = False
    installation_evidence_verified = False

    try:
        candidate_schema = _schema(expected_schema, binding['candidate']['revision'])
        predecessor_schema = _schema(expected_schema, binding['predecessor']['revision'])
        snapshot = _remote_snapshot(ssh, remote_runtime, binding['operation'], binding['operationId'])
        journal_state = snapshot.get('journalState') or 'none'
        if binding['operation'] in {'upgrade', 'recover'}:
            request = snapshot.get('request')
            journal = snapshot.get('journal')
            identity = journal.get('identity') if isinstance(journal, dict) else None
            host_journal_verified = bool(
                isinstance(request, dict) and isinstance(identity, dict)
                and identity.get('artifactAdmissionDigest') == binding['operationId']
                and identity.get('candidate') == binding['candidate']['image']
                and identity.get('predecessor') == binding['predecessor']['image'])
        classification, observed, observed_schema = _verify_snapshot(
            snapshot, binding, candidate_schema, predecessor_schema)
        if binding['operation'] == 'deploy' and classification is not None:
            journal_state = 'image-only-' + classification
        runtime = snapshot['runtime']
        host_evidence_type = runtime.get('evidenceType')
        container_image_verified = (
            runtime.get('image') == observed['image']
            and runtime.get('revision') == observed['revision']
            and isinstance(runtime.get('containerImageID'), str)
            and runtime['image'] in runtime.get('repositoryDigests', []))
        common_runtime_verified = container_image_verified and all((
            runtime.get('descriptorImage') == observed['image'],
            runtime.get('markerImage') == observed['image'],
        ))
        deployment_receipt_verified = bool(
            host_evidence_type == 'deployment'
            and runtime.get('receiptImage') == observed['image']
            and runtime.get('receiptRevision') == observed['revision'])
        installation_evidence_verified = bool(
            host_evidence_type == 'installation'
            and classification == 'committed'
            and binding['operation'] == 'deploy'
            and binding['candidate'] == binding['predecessor'])
        host_runtime_verified = common_runtime_verified and (
            deployment_receipt_verified or installation_evidence_verified)
        if observed_schema not in (candidate_schema, predecessor_schema):
            classification = None
        if classification is not None:
            checked = verify_public_revision(observed['revision'])
            if checked is False:
                raise ValueError('Authenticated public revision check did not confirm the runtime')
            public_identity_verified = True
            result = classification
        else:
            failure = 'Host state does not match a terminal candidate or recovered predecessor'
    except Exception as exc:
        # Do not copy remote stderr, credentials, or callback details into the
        # receipt. The workflow log already carries its own diagnostic context.
        failure = type(exc).__name__

    outcome = {
        'version': 1,
        'deploymentId': deployment_id,
        'operation': binding['operation'],
        'operationId': binding['operationId'],
        'candidate': binding['candidate'],
        'predecessor': binding['predecessor'],
        'result': result,
        'observed': observed,
        'journalState': journal_state,
        'hostJournalVerified': host_journal_verified,
        'hostRuntimeVerified': host_runtime_verified,
        'hostEvidenceType': host_evidence_type,
        'deploymentReceiptVerified': deployment_receipt_verified,
        'installationEvidenceVerified': installation_evidence_verified,
        'containerImageVerified': container_image_verified,
        'publicIdentityVerified': public_identity_verified,
        'candidateSchema': candidate_schema,
        'predecessorSchema': predecessor_schema,
        'observedSchema': observed_schema,
        'schemaVerified': bool(
            result == 'committed' and observed_schema == candidate_schema
            or result == 'recovered' and observed_schema == predecessor_schema),
    }
    if failure:
        outcome['verificationError'] = failure
    _write_atomic(output_path, outcome)
    return outcome
