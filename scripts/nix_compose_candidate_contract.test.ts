import { expect, test } from 'bun:test'
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { parse } from 'yaml'

const composeCandidateWorkflow = parse(readFileSync('.github/workflows/nix-compose-candidate.yml', 'utf8'))
const ghExpr = (expression: string): string => '$' + '{{ ' + expression + ' }}'
const workflowInlinePython = (run: string): string => {
  const match = run.match(/python3 - <<'PY'\n([\s\S]*?)\nPY/)
  if (!match) throw new Error('workflow step has no embedded Python contract')
  return match[1]
}

test('Compose release authorization binds successful main run, source ancestry and exact artifact attempt', () => {
  expect(Object.keys(composeCandidateWorkflow.on)).toEqual(['workflow_dispatch'])
  expect(Object.keys(composeCandidateWorkflow.on.workflow_dispatch.inputs)).toEqual(['release_run_id'])
  const authorizeJob = composeCandidateWorkflow.jobs.authorize
  expect(authorizeJob.if).toBe("github.repository == 'flidai/leapview' && github.ref == 'refs/heads/main'")
  const checkout = authorizeJob.steps.find((step: any) => step.uses?.startsWith('actions/checkout@'))
  expect(checkout.with.ref).toBe(ghExpr('github.sha'))
  expect(checkout.with['fetch-depth']).toBe(0)
  expect(checkout.with['persist-credentials']).toBe(false)
  const authorize = authorizeJob.steps.find((step: any) => step.id === 'authorize')
  expect(authorize.run).toContain('actions/runs/$RELEASE_RUN_ID')
  expect(authorize.run).toContain('actions/workflows/release.yml')
  expect(authorize.run).toContain('actions/runs/$RELEASE_RUN_ID/artifacts')
  expect(authorize.run).toContain('qualification.verify_release_run')

  const root = mkdtempSync(join(tmpdir(), 'nix-compose-release-gate-'))
  try {
    const revisionResult = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: process.cwd(), encoding: 'utf8' })
    if (revisionResult.status !== 0) throw new Error(revisionResult.stderr)
    const revision = revisionResult.stdout.trim()
    const releaseRunId = 845001
    const runAttempt = 2
    const runData = {
      repository: { full_name: 'flidai/leapview' }, workflow_id: 713, id: releaseRunId,
      run_attempt: runAttempt, event: 'workflow_dispatch', status: 'completed',
      conclusion: 'success', head_branch: 'main', head_sha: revision,
    }
    const workflowData = { id: 713, path: '.github/workflows/release.yml' }
    const artifact = {
      id: 901,
      name: 'release-candidate-candidate-' + releaseRunId + '-' + runAttempt,
      digest: 'sha256:' + 'a'.repeat(64),
      expired: false,
      workflow_run: { id: releaseRunId, head_branch: 'main', head_sha: revision },
    }
    const execute = (run: any, artifactValue: any) => {
      writeFileSync(join(root, 'release-run-api.json'), JSON.stringify(run))
      writeFileSync(join(root, 'release-workflow-api.json'), JSON.stringify(workflowData))
      writeFileSync(join(root, 'release-artifact-pages.json'), JSON.stringify([{ artifacts: [artifactValue] }]))
      let script = workflowInlinePython(authorize.run)
      script = script
        .replace('sys.path.insert(0, "scripts")', 'sys.path.insert(0, ' + JSON.stringify(resolve('scripts')) + ')')
        .replace('source_root=Path("."),', 'source_root=Path(' + JSON.stringify(process.cwd()) + '),')
        .replace('Path("release-run-binding.json").write_text(', 'Path(os.environ["TEST_OUTPUT_DIR"], "release-run-binding.json").write_text(')
      return spawnSync('python3', ['-c', script], {
        cwd: process.cwd(),
        encoding: 'utf8',
        env: {
          ...process.env,
          RUNNER_TEMP: root,
          TEST_OUTPUT_DIR: root,
          GITHUB_SHA: revision,
          GITHUB_OUTPUT: join(root, 'outputs'),
        },
      })
    }
    const accepted = execute(runData, artifact)
    if (accepted.status !== 0) throw new Error(accepted.stdout + accepted.stderr)
    const binding = JSON.parse(readFileSync(join(root, 'release-run-binding.json'), 'utf8'))
    expect(binding).toMatchObject({
      releaseRunId, releaseRunAttempt: runAttempt, releaseArtifactId: artifact.id,
      releaseArtifactDigest: artifact.digest, releaseWorkflowPath: '.github/workflows/release.yml',
      releaseBranch: 'main', protectedWorkflowRevision: revision, releaseAdmission: false,
    })
    expect(readFileSync(join(root, 'outputs'), 'utf8')).toContain('release_artifact_id=901')
    expect(authorizeJob.steps.find((step: any) => step.id === 'binding').run)
      .toContain('sha256=')

    expect(execute({ ...runData, head_branch: 'feature' }, {
      ...artifact, workflow_run: { ...artifact.workflow_run, head_branch: 'feature' },
    }).status).not.toBe(0)
    expect(execute(runData, { ...artifact, name: 'release-candidate-candidate-' + releaseRunId + '-1' }).status).not.toBe(0)
    expect(execute({ ...runData, conclusion: 'failure' }, artifact).status).not.toBe(0)

    const bindingRoot = join(root, 'binding-step')
    const bindingPath = join(bindingRoot, 'release-input', 'release-run-binding.json')
    mkdirSync(join(bindingRoot, 'release-input'), { recursive: true })
    writeFileSync(bindingPath, '{"releaseAdmission":false}\n')
    const bindingStep = authorizeJob.steps.find((step: any) => step.id === 'binding')
    const bindingOutput = join(bindingRoot, 'github-output')
    const bindingRun = spawnSync('bash', ['-c', bindingStep.run], {
      cwd: bindingRoot,
      encoding: 'utf8',
      env: { ...process.env, GITHUB_OUTPUT: bindingOutput },
    })
    if (bindingRun.status !== 0) throw new Error(bindingRun.stdout + bindingRun.stderr)
    const digest = spawnSync('sha256sum', [bindingPath], { encoding: 'utf8' }).stdout.split(/\s+/)[0]
    expect(readFileSync(bindingOutput, 'utf8')).toBe('sha256=' + digest + '\n')
  } finally {
    rmSync(root, { recursive: true, force: true })
  }

  const rawDownload = authorizeJob.steps.find((step: any) => step.name?.includes('digest-check'))
  expect(rawDownload.run).toContain('actions/artifacts/$ARTIFACT_ID/zip')
  expect(rawDownload.run).toContain('--artifact-digest \"$ARTIFACT_DIGEST\"')
  expect(rawDownload.run).toContain('--release-authorization release-run-binding.json')
  expect(rawDownload.run).toContain('release-candidate.zip')
})

test('Compose image preflight is credentialed only for image admission and precedes controller execution', () => {
  const { preflight, qualify, 'build-bundles': build } = composeCandidateWorkflow.jobs
  expect(preflight.needs).toEqual(['authorize', 'build-bundles'])
  expect(preflight.strategy.matrix.include.map((row: any) => [row.arch, row.runner]))
    .toEqual([['amd64', 'ubuntu-24.04'], ['arm64', 'ubuntu-24.04-arm']])
  expect(preflight.permissions).toEqual({
    actions: 'read', attestations: 'read', contents: 'read', packages: 'read',
  })
  expect(preflight.steps.find((step: any) => step.uses === './.github/actions/oci-admission').with)
    .toMatchObject({
      image: ghExpr('needs.build-bundles.outputs.image_reference'),
      'expected-workflow': 'flidai/leapview/.github/workflows/release.yml',
      'source-revision': ghExpr('needs.authorize.outputs.source_revision'),
      platform: 'linux/' + ghExpr('matrix.arch'),
    })
  const preflightRuntime = preflight.steps.findIndex((step: any) => step.name?.includes('actual release image runtime'))
  const preflightUpload = preflight.steps.findIndex((step: any) => step.name?.includes('immutable pre-execution'))
  expect(preflightRuntime).toBeLessThan(preflightUpload)
  expect(preflight.steps[preflightRuntime].run).toContain('docker run --rm \"$IMAGE_REFERENCE\" version --json')
  expect(preflight.steps.some((step: any) => step.run?.includes('leapviewctl'))).toBe(false)
  expect(preflight.steps.some((step: any) => step.run?.includes('installed-candidate'))).toBe(false)
  const preflightDiagnostics = preflight.steps.find((step: any) => step.name?.includes('admission diagnostics'))
  expect(preflightDiagnostics.if).toBe('always()')
  expect(preflightDiagnostics.with['if-no-files-found']).toBe('warn')
  expect(build.outputs.image_reference).toBe(ghExpr('steps.image.outputs.reference'))

  expect(qualify.needs).toEqual(['authorize', 'build-bundles', 'preflight', 'controller-evidence'])
  expect(qualify['timeout-minutes']).toBe(270)
  expect(qualify.permissions).toEqual({ actions: 'read', contents: 'read' })
  expect(qualify.environment).toBeUndefined()
  expect(qualify.steps.some((step: any) => step.uses === './.github/actions/oci-admission')).toBe(false)
  expect(qualify.steps.some((step: any) => step.uses?.startsWith('docker/login-action@'))).toBe(false)
  const idResolver = qualify.steps.find((step: any) => step.id === 'preflight')
  expect(idResolver.run).toContain('nix-compose-preflight-{run_id}-{attempt}-{arch}')
  expect(idResolver.run).toContain('head_branch')
  expect(idResolver.run).toContain('head_sha')
  expect(idResolver.run).toContain('item.get(\"expired\") is False')
  const evidenceResolver = qualify.steps.find((step: any) => step.id === 'controller-evidence')
  expect(evidenceResolver.run).toContain('nix-compose-controller-evidence-{run_id}-{attempt}-{arch}')
  expect(evidenceResolver.run).toContain('head_sha')
  expect(evidenceResolver.run).toContain('item.get(\"expired\") is False')
  const downloads = qualify.steps.filter((step: any) => step.uses?.startsWith('actions/download-artifact@'))
  expect(downloads.map((step: any) => step.with['artifact-ids'])).toEqual([
    ghExpr('needs.build-bundles.outputs.artifact_id'),
    ghExpr('needs.authorize.outputs.handoff_artifact_id'),
    ghExpr('steps.preflight.outputs.artifact_id'),
    ghExpr('steps.controller-evidence.outputs.artifact_id'),
  ])
  const checks = qualify.steps.find((step: any) => step.name?.includes('Verify authorized source'))
  expect(checks.run).toContain('cmp release-input/release-run-binding.json')
  expect(checks.run).toContain('cmp release-input/image-reference.txt')
  expect(checks.run).toContain('preflight/$ARCH/oci-admission.json')
  const controllerEvidenceCheck = qualify.steps.find((step: any) =>
    step.name?.includes('Verify exact controller security evidence'))
  const candidateVersion = qualify.steps.find((step: any) =>
    step.name?.includes('extracted controller runtime identity'))
  expect(controllerEvidenceCheck.run).toContain('verify-controller-evidence')
  expect(controllerEvidenceCheck.run).toContain('--binary-verifier')
  expect(qualify.steps.indexOf(controllerEvidenceCheck)).toBeLessThan(qualify.steps.indexOf(candidateVersion))
  const journey = qualify.steps.find((step: any) => step.name?.includes('full installed-candidate journey'))
  expect(journey.run).toContain('--multi-node-process')
  expect(journey.run).toContain('env -u GH_TOKEN -u GITHUB_TOKEN')
  const record = qualify.steps.find((step: any) => step.name?.includes('write the success receipt'))
  expect(record.run).toContain('record-qualification')
  expect(record.run).toContain('--release-artifact-zip release-input/release-candidate.zip')
  expect(record.run).toContain('--qualification-evidence-dir \"$EVIDENCE_ROOT\"')
  expect(record.run).toContain('--controller-evidence-dir \"controller-evidence/$ARCH\"')
  expect(record.run).toContain('--controller-binary-verifier')
  expect(record.run).toContain('--runtime-identity \"candidate/$ARCH/image-runtime-identity.json\"')
  expect(qualify.steps.find((step: any) => step.name?.includes('success-qualified')).if).toBe('success()')
  const diagnostics = qualify.steps.find((step: any) => step.name?.includes('diagnostics even on failure'))
  expect(diagnostics.if).toBe('always()')
  expect(diagnostics.with['if-no-files-found']).toBe('warn')
})

test('Compose controller evidence runs natively before installation with no signing authority', () => {
  const evidence = composeCandidateWorkflow.jobs['controller-evidence']
  expect(evidence.needs).toEqual(['authorize', 'build-bundles'])
  expect(evidence.strategy.matrix.include.map((row: any) => [row.arch, row.runner]))
    .toEqual([['amd64', 'ubuntu-24.04'], ['arm64', 'ubuntu-24.04-arm']])
  expect(evidence['timeout-minutes']).toBe(150)
  expect(evidence.permissions).toEqual({ contents: 'read' })
  expect(evidence.environment).toBeUndefined()
  const downloads = evidence.steps.filter((step: any) => step.uses?.startsWith('actions/download-artifact@'))
  expect(downloads.map((step: any) => step.with['artifact-ids']))
    .toEqual([ghExpr('needs.build-bundles.outputs.artifact_id'), ghExpr('needs.authorize.outputs.handoff_artifact_id')])
  const collect = evidence.steps.find((step: any) => step.name?.includes('Collect native Go, SPDX'))
  expect(collect.run).toContain('scripts/nix_compose_controller_evidence.py qualify-controller')
  expect(collect.run).toContain('--binary-verifier')
  expect(collect.run).toContain('--platform "linux/$ARCH"')
  expect(evidence.steps.some((step: any) => step.run?.includes('qualify installed-candidate'))).toBe(false)
  expect(evidence.steps.some((step: any) => step.run?.includes('leapviewctl version'))).toBe(false)
  expect(evidence.steps.find((step: any) => step.name?.includes('successful native controller evidence')).if).toBe('success()')
  const diagnostics = evidence.steps.find((step: any) => step.name?.includes('evidence diagnostics'))
  expect(diagnostics.if).toBe('always()')
  expect(diagnostics.with.name).toContain('${{ github.run_id }}-${{ github.run_attempt }}-${{ matrix.arch }}')
})

test('Compose evidence verifier tests run in core and hosted Nix CI when helper paths change', () => {
  const command = 'python3 -m unittest discover -s scripts/tests -p test_nix_compose_controller_evidence.py'
  expect(readFileSync('Taskfile.yml', 'utf8')).toContain(command)
  const workflow = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  expect(workflow.on.pull_request.paths).toContain('scripts/nix_compose_controller_evidence.py')
  expect(workflow.on.pull_request.paths).toContain('scripts/tests/test_nix_compose_controller_evidence.py')
  expect(workflow.jobs.image.steps.some((step: any) => step.run?.includes(command))).toBe(true)
})

test('fresh-host verifier generates protected Go inputs before its static build', () => {
  const step = composeCandidateWorkflow.jobs['host-qualification'].steps.find((item: any) =>
    item.name === 'Build the protected first-publication verifier')
  const root = mkdtempSync(join(tmpdir(), 'nix-compose-protected-build-'))
  const bin = join(root, 'bin')
  mkdirSync(bin)
  const executable = (name: string, script: string) =>
    writeFileSync(join(bin, name), '#!/bin/sh\nset -eu\n' + script, { mode: 0o755 })
  try {
    executable('git', 'case "$*" in "rev-parse HEAD") printf "%s\\n" "$FIXTURE_REVISION";; "status --porcelain --untracked-files=no") :;; *) exit 91;; esac\n')
    executable('nix', 'test "$1" = develop; shift; test "$1" = --no-update-lock-file; shift; test "$1" = -c; shift; exec "$@"\n')
    executable('task', 'test "$PWD" = "$FIXTURE_ROOT"; test "$*" = generate; test "${FAIL_GENERATION:-0}" = 0; touch generated-inputs\n')
    executable('go', 'test "$PWD" = "$FIXTURE_ROOT"; test -f generated-inputs; test "$CGO_ENABLED" = 0; test "$*" = "build -trimpath -o $RUNNER_TEMP/host-first-publication-verifier ./cmd/leapviewctl"; printf "protected verifier\\n" > "$RUNNER_TEMP/host-first-publication-verifier"\n')
    const run = (extra: Record<string, string> = {}) => spawnSync('bash', ['-c', step.run], {
      cwd: root,
      encoding: 'utf8',
      env: {
        ...process.env, PATH: bin + ':' + process.env.PATH, RUNNER_TEMP: root,
        FIXTURE_ROOT: root, FIXTURE_REVISION: 'a'.repeat(40), PROTECTED_REVISION: 'a'.repeat(40), ...extra,
      },
    })
    const result = run()
    expect(result.status, result.stdout + result.stderr).toBe(0)
    expect(readFileSync(join(root, 'host-first-publication-verifier'), 'utf8')).toBe('protected verifier\n')
    rmSync(join(root, 'generated-inputs'))
    rmSync(join(root, 'host-first-publication-verifier'))
    expect(run({ FAIL_GENERATION: '1' }).status).not.toBe(0)
    expect(run({ PROTECTED_REVISION: 'b'.repeat(40) }).status).not.toBe(0)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('fresh-host first publication uses a protected verifier and protected authoring assets', () => {
  const host = composeCandidateWorkflow.jobs['host-qualification']
  const buildVerifier = host.steps.find((step: any) => step.name?.includes('Build the protected first-publication verifier'))
  const qualifyGuest = host.steps.find((step: any) => step.name?.includes('qualify a fresh disposable guest'))
  expect(buildVerifier).toBeDefined()
  expect(buildVerifier.run).toContain('git rev-parse HEAD')
  expect(buildVerifier.run).toContain('$PROTECTED_REVISION')
  expect(buildVerifier.run).toContain('CGO_ENABLED=0')
  expect(buildVerifier.run).toContain('./cmd/leapviewctl')
  expect(host.steps.indexOf(buildVerifier)).toBeLessThan(host.steps.indexOf(qualifyGuest))
  expect(qualifyGuest.env.PROTECTED_REVISION).toBe(ghExpr('github.sha'))
  expect(qualifyGuest.run).toContain('--protected-root . --protected-revision "$PROTECTED_REVISION"')
  expect(qualifyGuest.run).toContain('--first-publication-verifier "$RUNNER_TEMP/host-first-publication-verifier"')
  expect(qualifyGuest.run).toContain("'domain': 'localhost'")
  expect(qualifyGuest.run).toContain("'environment': 'prod'")
  expect(qualifyGuest.run).toContain("'https': True")

  const hostGuest = readFileSync('scripts/nix_compose_host_guest.py', 'utf8')
  expect(hostGuest).toContain('qualify first-publication --evidence-dir')
  expect(hostGuest).toContain('--assets-root')
  expect(hostGuest).toContain('https://localhost/readyz')
  expect(hostGuest).toContain('/opt/leapview/current/leapviewctl activate-first-install')
  expect(hostGuest).toContain('HostConfig.PortBindings')
  expect(hostGuest).toContain('private-bootstrap')
  expect(hostGuest).toContain('publicCaddy')
  expect(hostGuest).toContain('_validate_first_install_lifecycle')
  expect(hostGuest).not.toContain('public-proxy-gating')
})

test('Compose signer anchors every qualified copy to original build and pre-execution artifacts', () => {
  const { sign, 'verify-attestations': verify } = composeCandidateWorkflow.jobs
  expect(sign.needs).toEqual(['authorize', 'build-bundles', 'preflight', 'controller-evidence', 'qualify'])
  expect(sign.if).toBe("github.repository == 'flidai/leapview' && github.ref == 'refs/heads/main'")
  expect(sign.environment).toBe('leapview-ephemeral-qualification')
  expect(sign.permissions).toEqual({
    actions: 'read', contents: 'read', attestations: 'write', 'id-token': 'write',
  })
  const resolver = sign.steps.find((step: any) => step.id === 'artifacts')
  expect(resolver.run).toContain('nix-compose-{kind}-{run_id}-{attempt}-{arch}')
  expect(resolver.run).toContain('head_branch')
  expect(resolver.run).toContain('head_sha')
  expect(resolver.run).toContain('sha256:[0-9a-f]{64}')
  expect(resolver.run).toContain('a.get(\"expired\") is False')
  const downloads = sign.steps.filter((step: any) => step.uses?.startsWith('actions/download-artifact@'))
  expect(downloads).toHaveLength(8)
  expect(downloads.some((step: any) => step.with.path === 'original-build' &&
    step.with['artifact-ids'] === ghExpr('needs.build-bundles.outputs.artifact_id'))).toBe(true)
  expect(downloads.some((step: any) => step.with.path === 'original-controller-evidence/amd64' &&
    step.with['artifact-ids'] === ghExpr('steps.artifacts.outputs.controller_evidence_amd64_id'))).toBe(true)
  expect(downloads.some((step: any) => step.with.path === 'original-controller-evidence/arm64' &&
    step.with['artifact-ids'] === ghExpr('steps.artifacts.outputs.controller_evidence_arm64_id'))).toBe(true)
  const offline = sign.steps.find((step: any) => step.name?.includes('Recompute receipts'))
  expect(offline.run).toContain('original-build/$arch/$file')
  expect(offline.run).toContain('preflight/$arch/$file')
  const compareControllerIndex = offline.run.indexOf('compare-controller-evidence')
  const verifyReceiptIndex = offline.run.indexOf('verify-qualification')
  expect(compareControllerIndex).toBeGreaterThan(0)
  expect(compareControllerIndex).toBeLessThan(verifyReceiptIndex)
  expect(offline.run).toContain('--left-evidence-dir "original-controller-evidence/$arch"')
  expect(offline.run).toContain('--right-evidence-dir "qualified/$arch/controller-evidence"')
  expect(offline.run).toContain('--controller-binary-verifier')
  expect(offline.run).toContain('verify-qualification')
  expect(offline.run).toContain('--qualification-evidence-dir')
  expect(offline.run).toContain('cmp release-input/release-run-binding.json')
  expect(offline.run).not.toContain('docker run')
  expect(offline.run).not.toContain('qualify installed-candidate')
  const attestSteps = sign.steps.filter((step: any) => step.uses?.startsWith('actions/attest@'))
  expect(attestSteps).toHaveLength(6)
  expect(attestSteps.filter((step: any) => step.with['predicate-type'] === 'https://spdx.dev/Document/v2.3')).toHaveLength(2)
  expect(attestSteps.every((step: any) => step.uses.endsWith('@1e69f48acb82d1966a394da916b4c1698aa569d6'))).toBe(true)

  expect(verify.needs).toEqual(['authorize', 'sign'])
  expect(verify.permissions).toEqual({ actions: 'read', attestations: 'read', contents: 'read' })
  expect(verify.environment).toBeUndefined()
  expect(verify.permissions['id-token']).toBeUndefined()
  expect(Object.values(verify.permissions).some((value: any) => value === 'write')).toBe(false)
  const download = verify.steps.find((step: any) => step.uses?.startsWith('actions/download-artifact@'))
  expect(download.with['artifact-ids']).toBe(ghExpr('needs.sign.outputs.artifact_id'))
  const live = verify.steps.find((step: any) => step.name?.includes('verify attestations read-only'))
  expect(live.run).toContain('--source-digest \"$GITHUB_SHA\" --source-ref refs/heads/main')
  expect(live.run).toContain('--signer-workflow flidai/leapview/.github/workflows/nix-compose-candidate.yml')
  expect(live.run).toContain('--deny-self-hosted-runners')
  expect(live.run).toContain('--predicate-type https://slsa.dev/provenance/v1 --format json --limit 10')
  expect(live.run).toContain('statement.get(\"subject\")')
  expect(live.run).toContain('subjects[0].get(\"digest\")')
  expect(live.run).toContain('archive_before')
  expect(live.run).toContain('receipt_before')
  expect(live.run).toContain('predicate-type https://spdx.dev/Document/v2.3')
  expect(live.run).toContain('canonical_json(statement.get("predicate")) != canonical_json(expected_spdx)')
  expect(live.run).toContain('controller-evidence/sbom.spdx.json')
  expect(verify.steps.find((step: any) => step.name?.includes('Retain independent'))?.if).toBe('always()')

  const externalUses = Object.values(composeCandidateWorkflow.jobs).flatMap((job: any) =>
    job.steps.flatMap((step: any) => step.uses?.startsWith('./') ? [] : step.uses ? [step.uses] : []))
  for (const use of externalUses) expect(use).toMatch(/@[a-f0-9]{40}$/)
  for (const job of Object.values(composeCandidateWorkflow.jobs) as any[]) {
    for (const step of job.steps.filter((item: any) => item.uses?.startsWith('actions/checkout@'))) {
      expect(step.with['persist-credentials']).toBe(false)
    }
  }
})

test('protected artifact API selector accepts exact current-attempt outputs and rejects substitutions', () => {
  const resolver = composeCandidateWorkflow.jobs.sign.steps.find((step: any) => step.id === 'artifacts')
  const script = workflowInlinePython(resolver.run)
  const root = mkdtempSync(join(tmpdir(), 'nix-compose-artifact-authority-'))
  const runId = '7755'
  const attempt = '3'
  const revision = 'a'.repeat(40)
  const artifact = (id: number, kind: string, arch: string) => ({
    id,
    name: 'nix-compose-' + kind + '-' + runId + '-' + attempt + '-' + arch,
    expired: false,
    digest: 'sha256:' + 'b'.repeat(64),
    workflow_run: { id: Number(runId), head_branch: 'main', head_sha: revision },
  })
  const rows = [
    artifact(9100, 'qualified', 'amd64'), artifact(9101, 'qualified', 'arm64'),
    artifact(9102, 'preflight', 'amd64'), artifact(9103, 'preflight', 'arm64'),
    artifact(9104, 'controller-evidence', 'amd64'), artifact(9105, 'controller-evidence', 'arm64'),
  ]
  const execute = (artifacts: any[]) => {
    writeFileSync(join(root, 'artifacts.json'), JSON.stringify([{ artifacts }]))
    return spawnSync('python3', ['-c', script], {
      encoding: 'utf8',
      env: {
        ...process.env, RUNNER_TEMP: root, GITHUB_OUTPUT: join(root, 'outputs'),
        EXPECTED_RUN_ID: runId, EXPECTED_RUN_ATTEMPT: attempt, EXPECTED_SOURCE_REVISION: revision,
      },
    })
  }
  try {
    const accepted = execute(rows)
    if (accepted.status !== 0) throw new Error(accepted.stdout + accepted.stderr)
    const output = readFileSync(join(root, 'outputs'), 'utf8')
    expect(output).toContain('qualified_amd64_id=9100')
    expect(output).toContain('preflight_arm64_id=9103')
    expect(output).toContain('controller_evidence_amd64_id=9104')
    expect(output).toContain('controller_evidence_arm64_id=9105')
    expect(execute(rows.map((row, index) => index === 3
      ? { ...row, workflow_run: { ...row.workflow_run, head_branch: 'feature' } } : row)).status).not.toBe(0)
    expect(execute(rows.map((row, index) => index === 2
      ? { ...row, workflow_run: { ...row.workflow_run, head_sha: 'c'.repeat(40) } } : row)).status).not.toBe(0)
    expect(execute([...rows, { ...rows[0], id: 9999 }]).status).not.toBe(0)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('native controller evidence selector accepts only the exact successful architecture artifact', () => {
  const resolver = composeCandidateWorkflow.jobs.qualify.steps.find((step: any) => step.id === 'controller-evidence')
  const script = workflowInlinePython(resolver.run)
  const root = mkdtempSync(join(tmpdir(), 'nix-compose-controller-evidence-authority-'))
  const runId = '8122'
  const attempt = '4'
  const revision = 'd'.repeat(40)
  const valid = {
    id: 8812,
    name: `nix-compose-controller-evidence-${runId}-${attempt}-amd64`,
    expired: false,
    digest: 'sha256:' + 'e'.repeat(64),
    workflow_run: { id: Number(runId), head_branch: 'main', head_sha: revision },
  }
  const execute = (artifact: any) => {
    writeFileSync(join(root, 'controller-evidence-artifacts.json'), JSON.stringify([{ artifacts: [artifact] }]))
    return spawnSync('python3', ['-c', script], {
      encoding: 'utf8',
      env: {
        ...process.env, RUNNER_TEMP: root, GITHUB_OUTPUT: join(root, 'outputs'),
        EXPECTED_RUN_ID: runId, EXPECTED_RUN_ATTEMPT: attempt,
        EXPECTED_SOURCE_REVISION: revision, ARCH: 'amd64',
      },
    })
  }
  try {
    const accepted = execute(valid)
    if (accepted.status !== 0) throw new Error(accepted.stdout + accepted.stderr)
    expect(readFileSync(join(root, 'outputs'), 'utf8')).toBe('artifact_id=8812\n')
    expect(execute({ ...valid, name: valid.name.replace(`-${attempt}-`, '-3-') }).status).not.toBe(0)
    expect(execute({ ...valid, expired: true }).status).not.toBe(0)
    expect(execute({ ...valid, digest: 'sha256:mutable' }).status).not.toBe(0)
    expect(execute({ ...valid, workflow_run: { ...valid.workflow_run, head_branch: 'feature' } }).status).not.toBe(0)
    expect(execute({ ...valid, workflow_run: { ...valid.workflow_run, head_sha: 'f'.repeat(40) } }).status).not.toBe(0)
    expect(execute({ ...valid, id: '8812' }).status).not.toBe(0)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('live SPDX verifier rejects predicate or exact outer bundle subject substitutions', () => {
  const live = composeCandidateWorkflow.jobs['verify-attestations'].steps
    .find((step: any) => step.name?.includes('verify attestations read-only'))
  const verifierScripts = [...live.run.matchAll(/python3 - [^\n]*<<'PY'\n([\s\S]*?)\nPY/g)]
    .map((match: any) => match[1])
  const verifierScript = verifierScripts.find((script: string) => script.includes('expected_spdx'))
  expect(verifierScript).toBeTruthy()
  const root = mkdtempSync(join(tmpdir(), 'nix-compose-live-spdx-'))
  const verification = join(root, 'verified.json')
  const inventory = join(root, 'sbom.spdx.json')
  const imageInventory = {
    spdxVersion: 'SPDX-2.3', SPDXID: 'SPDXRef-DOCUMENT', filesAnalyzed: 1,
    packages: [{ name: 'leapviewctl' }],
  }
  const archiveName = 'leapview-compose-candidate-8122-4-linux-amd64.tar.gz'
  const archiveDigest = 'c'.repeat(64)
  const record = (statement: any) => ({ verificationResult: { statement } })
  const statement = {
    _type: 'https://in-toto.io/Statement/v1',
    subject: [{ name: archiveName, digest: { sha256: archiveDigest } }],
    predicateType: 'https://spdx.dev/Document/v2.3',
    predicate: imageInventory,
  }
  const execute = (records: any[]) => {
    writeFileSync(verification, JSON.stringify(records))
    return spawnSync('python3', ['-c', verifierScript, verification, archiveName, archiveDigest, inventory], {
      encoding: 'utf8',
    })
  }
  const executeRaw = (raw: string) => {
    writeFileSync(verification, raw)
    return spawnSync('python3', ['-c', verifierScript, verification, archiveName, archiveDigest, inventory], {
      encoding: 'utf8',
    })
  }
  try {
    writeFileSync(inventory, JSON.stringify(imageInventory))
    const accepted = execute([record(statement)])
    if (accepted.status !== 0) throw new Error(accepted.stdout + accepted.stderr)
    expect(execute([record({ ...statement, subject: [{
      name: 'other.tar.gz', digest: { sha256: archiveDigest },
    }] })]).status).not.toBe(0)
    expect(execute([record({ ...statement, subject: [{
      name: archiveName, digest: { sha256: 'a'.repeat(64) },
    }] })]).status).not.toBe(0)
    expect(execute([record({ ...statement, predicate: { ...imageInventory, packages: [] } })]).status).not.toBe(0)
    expect(execute([record({
      ...statement, predicate: { ...imageInventory, filesAnalyzed: true },
    })]).status).not.toBe(0)
    expect(execute([record({ ...statement, subject: [...statement.subject, ...statement.subject] })]).status).not.toBe(0)
    expect(execute([]).status).not.toBe(0)

    const duplicatePredicateType = JSON.stringify(statement).replace(
      '"predicateType":"https://spdx.dev/Document/v2.3"',
      '"predicateType":"https://spdx.dev/Document/v2.3","predicateType":"https://spdx.dev/Document/v2.3"',
    )
    expect(executeRaw(`[{"verificationResult":{"statement":${duplicatePredicateType}}}]`).status).not.toBe(0)
    const nonFinitePredicate = JSON.stringify(statement).replace('"filesAnalyzed":1', '"filesAnalyzed":NaN')
    expect(executeRaw(`[{"verificationResult":{"statement":${nonFinitePredicate}}}]`).status).not.toBe(0)
    writeFileSync(inventory, '{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","packages":[],"packages":[]}')
    expect(execute([record(statement)]).status).not.toBe(0)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('signer comparison accepts the original build binding and rejects extracted binding substitution', () => {
  const signer = composeCandidateWorkflow.jobs.sign.steps.find((step: any) => step.name?.includes('Recompute receipts'))
  const qualifier = composeCandidateWorkflow.jobs.qualify.steps.find((step: any) => step.name?.includes('Verify exact bundle'))
  expect(qualifier.run).toContain('RUNTIME_BUNDLE_BINDING')
  expect(qualifier.run).toContain('--output "$RUNTIME_BUNDLE_BINDING"')

  const start = signer.run.indexOf('for arch in amd64 arm64; do')
  const end = signer.run.indexOf('count="$(find', start)
  expect(start).toBeGreaterThanOrEqual(0)
  expect(end).toBeGreaterThan(start)
  const comparisons = signer.run.slice(start, end)
    .replaceAll('${{ needs.authorize.outputs.release_run_id }}', '7755')
    .replaceAll('${{ needs.authorize.outputs.release_run_attempt }}', '3')
  const root = mkdtempSync(join(tmpdir(), 'nix-compose-build-binding-'))
  const buildFiles = [
    'PACKAGE.tar.gz', 'PACKAGE.tar.gz.sha256', 'controller-build-identity.json',
    'static-compatibility.json', 'release-identity.json', 'image-reference.txt',
    'release-artifact-admission.json', 'release-run-binding.json', 'bundle-binding.json',
  ]
  const preflightFiles = [
    'release-identity.json', 'image-reference.txt', 'assembled-image-admission.json',
    'release-run-binding.json', 'oci-admission.json', 'image-runtime-identity.json',
  ]
  const files = (directory: string, names: string[], valueFor: (name: string) => string) => {
    mkdirSync(directory, { recursive: true })
    for (const name of names) writeFileSync(join(directory, name), valueFor(name))
  }
  const originalBinding = '{"source":"original Nix build verifier"}\n'
  const extractedBinding = '{"source":"runtime extraction","extractedController":"candidate bytes"}\n'
  try {
    const fakeBin = join(root, 'bin')
    mkdirSync(fakeBin, { recursive: true })
    writeFileSync(join(fakeBin, 'nix'), '#!/bin/sh\nexit 0\n', { mode: 0o755 })
    writeFileSync(join(root, 'release-run-binding.json'), '{"binding":"exact"}\n')
    mkdirSync(join(root, 'release-input'), { recursive: true })
    copyFileSync(join(root, 'release-run-binding.json'), join(root, 'release-input/release-run-binding.json'))
    for (const arch of ['amd64', 'arm64']) {
      const build = buildFiles.map((name) => name.replace('PACKAGE', `leapview-compose-candidate-7755-3-linux-${arch}`))
      const buildValue = (name: string) => name === 'bundle-binding.json' ? originalBinding
        : name === 'release-run-binding.json' ? '{"binding":"exact"}\n'
          : ['release-identity.json', 'image-reference.txt'].includes(name) ? `handoff:${name}\n`
            : `original:${name}\n`
      const preflightValue = (name: string) => name === 'release-run-binding.json'
        ? '{"binding":"exact"}\n'
        : ['release-identity.json', 'image-reference.txt'].includes(name) ? `handoff:${name}\n`
          : `preflight:${name}\n`
      files(join(root, 'original-build', arch), build, buildValue)
      files(join(root, 'qualified', arch), [...build, ...preflightFiles], (name) =>
        name === 'bundle-binding.json' ? originalBinding
          : preflightFiles.includes(name) ? preflightValue(name) : buildValue(name))
      files(join(root, 'preflight', arch), preflightFiles, preflightValue)
      files(join(root, 'original-controller-evidence', arch), ['controller-evidence.json'], () => '{"evidence":"original"}\n')
      files(join(root, 'qualified', arch, 'controller-evidence'), ['controller-evidence.json'], () => '{"evidence":"original"}\n')
    }

    const execute = () => spawnSync('bash', ['-c', 'set -euo pipefail\n' + comparisons + '\ndone\n'], {
      cwd: root, encoding: 'utf8', env: {
        ...process.env, PATH: `${fakeBin}:${process.env.PATH}`, SOURCE_REVISION: 'd'.repeat(40),
        RUNNER_TEMP: root,
      },
    })
    const original = execute()
    if (original.status !== 0) throw new Error(original.stdout + original.stderr)

    writeFileSync(join(root, 'qualified/amd64/bundle-binding.json'), extractedBinding)
    expect(execute().status).not.toBe(0)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('Compose assembly reads both named Nix output links before packaging', () => {
  const build = composeCandidateWorkflow.jobs['build-bundles']
  const assemble = build.steps.find((step: any) => step.name === 'Assemble and verify with protected tools')
  const packaging = assemble.run.indexOf('python3 scripts/package_compose_bundle.py')
  expect(packaging).toBeGreaterThan(0)
  const root = mkdtempSync(join(tmpdir(), 'compose-nix-output-links-'))
  try {
    // Nix appends a non-default output name to --out-link. The arm64
    // derivation therefore produces result-compose-cli-arm64, not the prefix.
    for (const arch of ['amd64', 'arm64']) {
      const output = join(root, `result-compose-cli-${arch}`)
      mkdirSync(output)
      writeFileSync(join(output, 'controller-build-identity.json'), JSON.stringify({ platform: `linux/${arch}` }))
      writeFileSync(join(output, 'static-compatibility.json'), JSON.stringify({ architecture: arch }))
    }
    const result = spawnSync('bash', ['-c', assemble.run.slice(0, packaging) + '\ndone\n'], {
      cwd: root,
      encoding: 'utf8',
      env: { ...process.env, RELEASE_RUN_ID: '123', RELEASE_RUN_ATTEMPT: '1' },
    })
    expect(result.stderr).toBe('')
    expect(result.status).toBe(0)
    for (const arch of ['amd64', 'arm64']) {
      expect(JSON.parse(readFileSync(join(root, 'candidate', arch, 'controller-build-identity.json'), 'utf8')))
        .toEqual({ platform: `linux/${arch}` })
      expect(JSON.parse(readFileSync(join(root, 'candidate', arch, 'static-compatibility.json'), 'utf8')))
        .toEqual({ architecture: arch })
    }
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
