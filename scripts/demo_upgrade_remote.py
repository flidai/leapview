#!/usr/bin/env python3
"""Root-only transport for candidate-owned upgrade commands; no SQL lives here."""
import json
import os
from pathlib import Path
import re
import subprocess
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
    if action == 'pending':
        print(json.dumps(pending_request(image, revision)))
        return
    if action not in ('plan', 'apply', 'recover'): raise ValueError('Unsupported operation')
    request = Path(sys.argv[4])
    data = json.loads(request.read_text())
    if data['candidateImage'] != image or data['candidateRevision'] != revision:
        raise ValueError('Request identity mismatch')
    binary = controller(image)
    os.execv(str(binary), [str(binary), 'host', 'upgrade', action, '--request', str(request)])


if __name__ == '__main__': main()
