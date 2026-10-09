"""Authenticate exact protected Nix site receipts before the existing Kamal owner mutates a host."""
import json
from pathlib import Path
import re
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'scripts'))
import managed_admission_handoff as handoff

WORKFLOW = 'flidai/leapview/.github/workflows/nix-output-admission.yml'
SELECTION_FIELDS = frozenset({'runId', 'runAttempt', 'artifactId', 'sourceRevision', 'producerRevision'})
DIGEST = re.compile(r'sha256:[a-f0-9]{64}\Z')
REVISION = re.compile(r'[a-f0-9]{40}\Z')


def validate_selection(selection):
    if (not isinstance(selection, dict) or set(selection) != SELECTION_FIELDS
            or any(type(selection.get(name)) is not int or selection[name] <= 0
                   for name in ('runId', 'runAttempt', 'artifactId'))
            or any(not isinstance(selection.get(name), str) or not REVISION.fullmatch(selection[name])
                   for name in ('sourceRevision', 'producerRevision'))):
        raise ValueError('exact protected Nix admission run, attempt, artifact and both revisions required')
    return selection


def validate_receipt(admission, image, selection):
    release, provenance = admission.get('release', {}), admission.get('provenance', {})
    profile = admission.get('nixEvidence', {})
    if (admission.get('version') != 'oci-artifact-admission/v1' or admission.get('decision') != 'admitted'
            or admission.get('repository') != 'ghcr.io/flidai/leapview-site'
            or admission.get('ociDigest') != image.split('@')[1]
            or admission.get('architectureMarker') != 'public-site/v1'
            or release.get('distribution') != 'nix' or release.get('platform') != 'linux/amd64'
            or release.get('image') != image or release.get('sourceRevision') != selection['sourceRevision']
            or provenance.get('verified') is not True or provenance.get('repository') != 'flidai/leapview'
            or provenance.get('sourceRevision') != selection['sourceRevision']
            or provenance.get('workflow') != 'flidai/leapview/.github/workflows/nix-site-candidate.yml'
            or admission.get('sbom', {}).get('verified') is not True
            or admission.get('securityPolicy', {}).get('passed') is not True
            or profile.get('version') != 'nix-oci-artifact-evidence/v1' or profile.get('kind') != 'site-image'
            or profile.get('verifierRevision') != selection['producerRevision']):
        raise ValueError('authenticated admission is not the exact selected AMD64 Nix site output')


def authenticate(directory, image, selection, run):
    validate_selection(selection)
    authorization, archive = handoff.fetch(selection['runId'], selection['runAttempt'],
        selection['artifactId'], selection['sourceRevision'], 'linux/amd64',
        producer_revision=selection['producerRevision'], kind='site-image')
    files = handoff.verify_bundle(archive, authorization['artifactDigest'], authorization, image=image)
    binding = handoff._strict_json(files['binding.json'])
    evidence = directory / 'nix-admission'
    evidence.mkdir(mode=0o700)
    for name, content in files.items():
        target = evidence / name
        target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        with target.open('xb') as stream:
            stream.write(content)
        target.chmod(0o600)
    command = ['go', 'run', './internal/app/tools/ociadmission', 'verify-receipt',
        '--bundle', str(evidence), '--image', image, '--source-revision', selection['sourceRevision'],
        '--platform', 'linux/amd64', '--expected-workflow', WORKFLOW,
        '--producer-revision', selection['producerRevision'], '--admission-digest', binding['admissionDigest']]
    if run(command).decode().strip() != binding['admissionDigest']:
        raise ValueError('Go canonical Nix receipt verification did not return the exact admission digest')
    admission = handoff._strict_json(files['admission.json'])
    validate_receipt(admission, image, selection)
    return admission, binding['admissionDigest'], authorization['artifactDigest']
