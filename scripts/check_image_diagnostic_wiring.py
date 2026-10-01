import json
import re
from pathlib import Path

action = Path('.github/actions/oci-admission/action.yml').read_text()
artifacts = Path('.github/workflows/artifacts.yml').read_text()
historical = Path('.github/workflows/demo-upgrade-qualification.yml').read_text()
diagnostic = Path('.github/workflows/image-qualification-diagnostics.yml').read_text()
policy = json.loads(Path('.github/security/container-vulnerability-policy.json').read_text())

assert '  vulnerability-report:' in action
report_input = action.split('  vulnerability-report:', 1)[1].split('\noutputs:', 1)[0]
assert 'required: false' in report_input
assert 'VULNERABILITY_REPORT: ${{ inputs.vulnerability-report }}' in action
assert 'vulnerability_report_args=(--vulnerability-report "$VULNERABILITY_REPORT")' in action
assert '"${vulnerability_report_args[@]}"' in action

for workflow in (artifacts, historical):
    assert 'platform: linux/amd64' in workflow
    assert 'vulnerability-report: ${{ runner.temp }}/oci-vulnerability-report.json' in workflow
    upload = workflow.split('- name: Upload OCI vulnerability diagnostic', 1)[1].split('\n      - name:', 1)[0]
    assert 'if: always()' in upload
    assert 'actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a' in upload
    assert 'path: ${{ runner.temp }}/oci-vulnerability-report.json' in upload
    assert 'if-no-files-found: ignore' in upload and 'retention-days: 14' in upload
    assert '${{ github.job }}' in upload and '${{ github.run_id }}' in upload and '${{ github.run_attempt }}' in upload
    assert '*' not in upload and 'evidence' not in upload.lower()

assert "if: github.event_name == 'workflow_dispatch'" in artifacts
assert 'Require the exact head of one open pull request to main' in artifacts
assert '.head.sha == $revision' in artifacts
assert policy['scanner'] == 'trivy' and policy['scannerVersion'] == '0.74.0'
assert re.fullmatch(r'aquasec/trivy:0\.74\.0@sha256:[0-9a-f]{64}', policy['scannerImage'])
assert policy['severity'] == ['CRITICAL', 'HIGH'] and policy['ignoreUnfixed'] is False
assert policy['maxUnresolved'] == 0
workflow_permissions = diagnostic.split('permissions:', 1)[1].split('jobs:', 1)[0]
assert 'write' not in workflow_permissions
assert '\n  workflow_dispatch:' not in diagnostic
print('Workflow/action wiring and pinned vulnerability policy validated.')
