#!/usr/bin/env python3
"""Read compatibility before OAuth, from source already bound to OCI admission."""
import json
import re
import subprocess

MANIFEST = 'internal/platform/releasecontract/contract.json'
LEGACY_REVISION = '28bfc7e7e8f8074847229c05c42f0bbc2dd79336'
LEGACY = dict(version=1, permissionProfile='legacy-capabilities/v1',
              publicationAPI='delivery/v1', hostTransition='compose-postgres-local/v1')


def read_contract(revision, git=None):
    if not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise ValueError('Release contract requires an immutable full source revision')
    if revision == LEGACY_REVISION:
        return dict(LEGACY)
    git = git or (lambda *args: subprocess.check_output(['git', *args], stderr=subprocess.PIPE))
    try:
        contract = json.loads(git('show', revision+':'+MANIFEST))
    except (subprocess.CalledProcessError, ValueError) as exc:
        raise ValueError('No supported release contract at '+revision) from exc
    expected = dict(version=1, permissionProfile='leapview.permissions/v1',
                    publicationAPI='delivery/v1', hostTransition='compose-postgres-local/v1')
    if not isinstance(contract, dict) or type(contract.get('version')) is not int or contract != expected:
        raise ValueError('Unsupported permission/publication/host transition contract')
    return contract


if __name__ == '__main__':
    import sys
    print(read_contract(sys.argv[1])['permissionProfile'])
