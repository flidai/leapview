#!/usr/bin/env python3
"""Root-only transport for candidate-owned upgrade commands; no SQL lives here."""
import json
import fcntl
import os
from pathlib import Path
import re
import subprocess
import stat
import sys
import tempfile

PROVIDER = Path('/etc/leapview-provider-cfo')
INSTALLATION = Path('/opt/leapview')
IMAGE = r'ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}'


def pending_request(image, revision):
    journal = INSTALLATION / 'upgrade-operation.json'
    state = json.loads(journal.read_text())['state']
    identity = state['identity']
    digest = identity['artifactAdmissionDigest']
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', digest):
        raise ValueError('Invalid persisted operation identity')
    request = json.loads((PROVIDER/'upgrade-operations'/digest[7:]/'request.json').read_text())
    if (identity['candidate'] != image or request['candidateImage'] != image or
            request['candidateRevision'] != revision or identity['target'] != 'app-leapview-demo-02'):
        raise ValueError('Recovery must name the interrupted candidate image and revision')
    return request


def prepared_request(image, revision, predecessor):
    matches = []
    for state_path in (PROVIDER/'upgrade-operations').glob('*/detached-rehearsal.json'):
        state = json.loads(state_path.read_text())
        identity = state.get('identity', {})
        if state.get('phase') != 'passed' or identity.get('candidate') != image or identity.get('predecessor') != predecessor:
            continue
        request = json.loads((state_path.parent/'request.json').read_text())
        if request['candidateRevision'] != revision:
            continue
        digest = identity.get('artifactAdmissionDigest', '')
        if not re.fullmatch(r'sha256:[0-9a-f]{64}', digest) or state_path.parent.name != digest[7:]:
            raise ValueError('Invalid private preparation identity')
        matches.append((state_path.stat().st_mtime_ns, digest))
    if not matches:
        raise ValueError('Run prepare for this exact predecessor and candidate before upgrade')
    print(json.dumps({'preparationDigest': max(matches)[1]}))


def controller(image):
    destination = PROVIDER/'upgrade-controllers'/image.rsplit(':', 1)[1]
    destination.mkdir(parents=True, exist_ok=True, mode=0o700)
    binary = destination/'leapviewctl'
    # Extract from the immutable candidate, not the runner checkout. A temporary
    # copy is atomically installed so interrupted extraction cannot poison retry.
    subprocess.run(['docker', 'pull', image], check=True, stdout=sys.stderr)
    cid = subprocess.check_output(['docker', 'create', image], text=True).strip()
    try:
        with tempfile.TemporaryDirectory(dir=destination) as directory:
            temporary = Path(directory)/'leapviewctl'
            subprocess.run(['docker', 'cp', cid+':/usr/local/libexec/leapviewctl', str(temporary)], check=True, stdout=sys.stderr)
            temporary.chmod(0o500)
            with temporary.open('rb') as stream:
                os.fsync(stream.fileno())
            os.replace(temporary, binary)
            fd = os.open(destination, os.O_RDONLY | os.O_DIRECTORY)
            try: os.fsync(fd)
            finally: os.close(fd)
    finally:
        subprocess.run(['docker', 'rm', '-v', cid], check=True, stdout=sys.stderr)
    return binary


def _private_object(path, label, maximum=1 << 20):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(descriptor)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or
                info.st_mode & 0o077 or info.st_size > maximum):
            raise ValueError('Invalid private '+label)
        with os.fdopen(os.dup(descriptor), 'rb') as stream:
            value = json.load(stream)
    finally:
        os.close(descriptor)
    if not isinstance(value, dict):
        raise ValueError('Invalid private '+label)
    return value


def _terminal_transition_request(image, revision, expected_digest):
    operation = PROVIDER/'upgrade-operations'/expected_digest[7:]
    directory = operation.lstat()
    if not stat.S_ISDIR(directory.st_mode) or directory.st_uid != os.geteuid() or directory.st_mode & 0o077:
        raise ValueError('Private transition operation directory required')
    request = _private_object(operation/'request.json', 'transition request')
    profile = request.get('profile')
    intent = request.get('accessTransition')
    if (request.get('version') != 1 or request.get('candidateImage') != image or
            request.get('candidateRevision') != revision or not isinstance(profile, dict) or
            profile.get('stateRoot') != str(PROVIDER)):
        raise ValueError('Persisted transition request differs from the admitted candidate')

    binary = PROVIDER/'upgrade-controllers'/image.rsplit(':', 1)[1]/'leapviewctl'
    info = binary.lstat()
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or
            stat.S_IMODE(info.st_mode) != 0o500):
        raise ValueError('Exact candidate transition controller is unavailable')
    plan = json.loads(subprocess.check_output([
        str(binary), 'host', 'upgrade', 'plan', '--request', str(operation/'request.json')]))
    if plan.get('operationDigest') != expected_digest:
        raise ValueError('Persisted transition request does not match the original operation')
    if not isinstance(intent, dict):
        return None

    terminal = []
    paths = [INSTALLATION/'upgrade-operation.json']
    history = INSTALLATION/'upgrade-history'
    if history.is_dir():
        paths.extend(sorted(history.glob('*.json')))
    for path in paths:
        try:
            state = _private_object(path, 'upgrade journal', 16384).get('state')
        except FileNotFoundError:
            continue
        if not isinstance(state, dict):
            raise ValueError('Invalid private upgrade journal state')
        identity = state.get('identity')
        if isinstance(identity, dict) and identity.get('artifactAdmissionDigest') == expected_digest:
            if (identity.get('candidate') != image or
                    identity.get('predecessor') != request.get('predecessorImage') or
                    identity.get('target') != profile.get('id')):
                raise ValueError('Terminal journal differs from the persisted transition request')
            terminal.append(state)

    if request.get('preparationDigest'):
        if not terminal or any(state.get('phase') not in ('committed', 'succeeded', 'recovered') for state in terminal):
            raise ValueError('Host transition journal is not terminal')
    else:
        # Capture can fail before creating a detached receipt, then durably
        # recover the unchanged predecessor. No clone can start without that
        # receipt, so its staged credentials are terminal too. Existing receipts
        # must still prove a matching completed rehearsal; malformed or active
        # receipts never authorize cleanup.
        if not terminal or any(state.get('phase') != 'recovered' for state in terminal):
            raise ValueError('Live capture journal is not terminal')
        try:
            detached = _private_object(operation/'detached-rehearsal.json', 'detached rehearsal receipt', 16384)
        except FileNotFoundError:
            detached = None
        if detached is not None and (detached.get('phase') not in ('passed', 'failed') or
                detached.get('identity') != terminal[0].get('identity')):
            raise ValueError('Detached rehearsal is still active or lacks its exact terminal receipt')
    if any(state != terminal[0] for state in terminal[1:]):
        raise ValueError('Conflicting terminal journal copies')
    return operation


def _clear_transition_files(operation_digest):
    suffix = operation_digest[7:][:16]
    name = 'leapview-recovery-'+suffix+'-transition'
    running = subprocess.check_output([
        'docker', 'ps', '--filter', 'name=^/'+name+'$', '--format', '{{.Names}}'], text=True).strip()
    if running:
        if running != name:
            raise ValueError('Unexpected transition container matched the cleanup selector')
        raise ValueError('Candidate access transition is still running; retaining its private inputs')

    operation = PROVIDER/'upgrade-operations'/operation_digest[7:]
    for filename in ('publisher.secret', 'reviewer.secret', 'transition-request.json',
                     'transition-journal.json', 'transition-publisher.secret',
                     'transition-reviewer.secret', 'transition.env'):
        path = operation/filename
        try:
            info = path.lstat()
        except FileNotFoundError:
            continue
        if not stat.S_ISREG(info.st_mode):
            raise ValueError('Transition cleanup encountered a non-regular private input')
        path.unlink()


def transition_credentials(action, image, revision, request_path, expected_digest):
    if not re.fullmatch(r'sha256:[0-9a-f]{64}', expected_digest):
        raise ValueError('Invalid reviewer operation binding')
    if action == 'clear-credentials':
        locks = []
        try:
            for path in (INSTALLATION/'.leapviewctl.lock', Path('/run/leapview-demo-deploy.lock')):
                try:
                    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
                except FileNotFoundError:
                    continue
                try:
                    fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
                except BaseException:
                    os.close(descriptor)
                    raise
                locks.append(descriptor)
            # Detached retries do not hold the live installation lock. Keep
            # their own lock while reading the terminal receipt and deleting
            # inputs, so a retry cannot start between those two operations.
            detached_lock = PROVIDER/'upgrade-operations'/expected_digest[7:]/'.detached-rehearsal.lock'
            descriptor = os.open(detached_lock, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
            try:
                fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BaseException:
                os.close(descriptor)
                raise
            locks.append(descriptor)
            operation = _terminal_transition_request(image, revision, expected_digest)
            if operation is not None:
                _clear_transition_files(expected_digest)
        finally:
            for descriptor in reversed(locks):
                fcntl.flock(descriptor, fcntl.LOCK_UN)
                os.close(descriptor)
        return
    destination = PROVIDER/'upgrade-operations'/expected_digest[7:]/'reviewer.secret'
    request = json.loads(Path(request_path).read_text())
    if (request['candidateImage'] != image or request['candidateRevision'] != revision or
            not request.get('accessTransition') or request['profile']['stateRoot'] != str(PROVIDER)):
        raise ValueError('Workload credentials require a matching access transition request')
    binary = controller(image)
    plan = json.loads(subprocess.check_output([str(binary), 'host', 'upgrade', 'plan', '--request', request_path]))
    if plan['operationDigest'] != expected_digest:
        raise ValueError('Workload credential operation mismatch')
    credentials = json.loads(sys.stdin.buffer.read(131073))
    if not isinstance(credentials, dict) or set(credentials) != {'publisher', 'reviewer'}:
        raise ValueError('Both bound workload credentials are required')
    for value in credentials.values():
        if not isinstance(value, str) or not value or len(value.encode()) > 65536 or any(ch in value for ch in '\x00\n\r'):
            raise ValueError('Invalid bounded workload credential')
    destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    info = destination.parent.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o077:
        raise ValueError('Private operation directory required')
    for role, credential in credentials.items():
        fd, temporary = tempfile.mkstemp(dir=destination.parent, prefix='.'+role+'-')
        try:
            with os.fdopen(fd, 'wb') as stream:
                stream.write(credential.encode())
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, destination.with_name(role+'.secret'))
        finally:
            Path(temporary).unlink(missing_ok=True)


def main():
    os.umask(0o077)
    if os.geteuid() != 0: raise ValueError('Root SSH is required')
    if os.environ.get('DOCKER_HOST') or os.environ.get('DOCKER_CONTEXT', 'default') != 'default':
        raise ValueError('Only the local Docker endpoint is supported')
    if subprocess.check_output(['docker', 'context', 'show'], text=True).strip() != 'default':
        raise ValueError('Only the default local Docker context is supported')
    action, image, revision = sys.argv[1:4]
    if not re.fullmatch(IMAGE, image) or not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise ValueError('Invalid immutable candidate')
    if action == 'stage-credentials':
        transition_credentials(action, image, revision, sys.argv[4], sys.argv[5])
        return
    if action == 'clear-credentials':
        transition_credentials(action, image, revision, '', sys.argv[4])
        return
    if action == 'intent':
        path = PROVIDER/'access-transition-intent.json'
        info = path.lstat()
        if not path.is_file() or path.is_symlink() or info.st_uid != 0 or info.st_mode & 0o077 or info.st_size > 1048576:
            raise ValueError('Access transition intent must be a bounded root-owned private file')
        print(json.dumps(json.loads(path.read_text())))
        return
    if action == 'prepared':
        predecessor = sys.argv[4]
        if not re.fullmatch(IMAGE, predecessor): raise ValueError('Invalid predecessor')
        prepared_request(image, revision, predecessor)
        return
    if action == 'pending':
        print(json.dumps(pending_request(image, revision)))
        return
    if action not in ('plan', 'apply', 'recover', 'capture', 'verify-copy'): raise ValueError('Unsupported operation')
    request = Path(sys.argv[4])
    data = json.loads(request.read_text())
    if data['candidateImage'] != image or data['candidateRevision'] != revision:
        raise ValueError('Request identity mismatch')
    binary = controller(image)
    os.execv(str(binary), [str(binary), 'host', 'upgrade', action, '--request', str(request)])


if __name__ == '__main__': main()
