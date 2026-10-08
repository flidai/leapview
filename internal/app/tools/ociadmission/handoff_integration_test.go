package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// This fixture exercises the language and process boundaries with fake producer
// tools and simulated GitHub API metadata. It never contacts GitHub or publishes
// a real admission. When run as root, publication uses only a fresh temp root.
func TestProducerHandoffIntegration(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is required for the authenticated transport integration")
	}
	args, env, bundle := bundleFixture(t)
	var output bytes.Buffer
	if err := runAdmission(args, env, &output, &output); err != nil {
		t.Fatal(err)
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source location unavailable")
	}
	packageDir := filepath.Dir(file)
	// Root validation may use a binary built by the ordinary development user
	// to avoid creating root-owned entries in that user's Go build cache.
	verifier := os.Getenv("LEAPVIEW_TEST_OCIADMISSION_VERIFIER")
	if verifier == "" {
		verifier = filepath.Join(t.TempDir(), "ociadmission")
		build := exec.Command("go", "build", "-o", verifier, ".")
		build.Dir = packageDir
		if data, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build verifier: %v\n%s", err, data)
		}
	}
	scripts := filepath.Clean(filepath.Join(packageDir, "../../../..", "scripts"))
	command := exec.Command(python, "-c", handoffIntegrationPython, bundle, verifier, scripts)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("producer/transport/verifier integration: %v\n%s", err, data)
	} else {
		t.Log(string(data))
	}
}

const handoffIntegrationPython = `
from datetime import datetime, timezone
import hashlib
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
from unittest.mock import patch
import zipfile

bundle, verifier, scripts = sys.argv[1:]
sys.path.insert(0, scripts)
import managed_admission_handoff as handoff

files = {path.name: path.read_bytes() for path in Path(bundle).iterdir()}
binding = json.loads(files['binding.json'])
archive = io.BytesIO()
with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as target:
    for name, data in files.items():
        target.writestr(name, data)
payload = archive.getvalue()
run_id, attempt = int(binding['runId']), int(binding['runAttempt'])
repository = {'full_name': 'flidai/leapview', 'id': 100}
run = {'id': run_id, 'run_attempt': attempt, 'workflow_id': 34,
       'path': binding['workflow'].removeprefix('flidai/leapview/'),
       'repository': repository, 'head_repository': repository,
       'event': binding['event'], 'status': 'completed', 'conclusion': 'success',
       'head_branch': 'main', 'head_sha': binding['sourceRevision'],
       'run_started_at': '2026-10-07T10:00:00Z', 'updated_at': '2026-10-07T11:00:00Z'}
workflow = {'id': 34, 'path': binding['workflow'].removeprefix('flidai/leapview/')}
artifact = {'id': 56, 'name': f'managed-admission-{run_id}-{attempt}-{binding["platform"].split("/")[1]}',
            'digest': 'sha256:' + hashlib.sha256(payload).hexdigest(), 'expired': False,
            'created_at': '2026-10-07T10:30:00Z', 'expires_at': '2099-01-01T00:00:00Z',
            'size_in_bytes': len(payload), 'workflow_run': {'id': run_id, 'repository_id': 100,
            'head_repository_id': 100, 'head_branch': 'main', 'head_sha': binding['sourceRevision']}}
with patch.object(handoff, '_github', side_effect=[json.dumps(run).encode(), json.dumps(workflow).encode(),
                                                  json.dumps(artifact).encode(), payload]) as github:
    authorization, downloaded = handoff.fetch(run_id, attempt, 56, binding['sourceRevision'], binding['platform'])
assert github.call_count == 4
verified = handoff.verify_bundle(downloaded, authorization['artifactDigest'], authorization, image=binding['image'])
assert verified == files
with tempfile.TemporaryDirectory(prefix='test-only-managed-handoff-') as directory:
    verified_dir = Path(directory, 'verified')
    verified_dir.mkdir(mode=0o700)
    for name, data in verified.items():
        (verified_dir / name).write_bytes(data)
    command = [verifier, 'verify-receipt', '--bundle', str(verified_dir), '--image', binding['image'],
               '--source-revision', binding['sourceRevision'], '--platform', binding['platform'],
               '--expected-workflow', authorization['workflow'], '--admission-digest', binding['admissionDigest']]
    result = subprocess.run(command, check=True, capture_output=True, text=True)
    assert result.stdout == binding['admissionDigest'] + '\n'
    if os.geteuid() == 0:
        root = Path(directory, 'private-authority')
        root.mkdir(mode=0o700)
        installed = handoff.install_receipt(root, binding['admissionDigest'], verified['admission.json'])
        assert installed.read_bytes() == verified['admission.json']
        assert installed.stat().st_uid == 0 and stat.S_IMODE(installed.stat().st_mode) == 0o400
        try:
            handoff.install_receipt(root, binding['admissionDigest'], b'replacement')
        except handoff.HandoffError:
            pass
        else:
            raise AssertionError('existing receipt was overwritten')
        assert installed.read_bytes() == verified['admission.json']
        print('fixture producer -> authenticated transport checks -> Go verifier -> private immutable installation passed')
    else:
        print('fixture producer -> authenticated transport checks -> Go verifier passed (root installation covered separately)')
`
