import { expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { parse } from 'yaml'

test('desktop attestations preserve original artifacts and isolate signing from candidate execution', () => {
  const { jobs } = parse(readFileSync('.github/workflows/nix-desktop-candidate.yml', 'utf8'))
  expect(jobs.sign).toBeDefined()
  expect(jobs['verify-signed']).toBeDefined()
  expect(jobs.sign.needs).toEqual(['authorize', 'build', 'qualify'])
  expect(jobs.sign.permissions).toEqual({ contents: 'read', 'pull-requests': 'read', attestations: 'write', 'id-token': 'write' })
  expect(jobs['verify-signed'].permissions).toEqual({ contents: 'read', attestations: 'read' })
  expect(jobs.sign.environment).toBe('leapview-ephemeral-qualification')
  for (const job of [jobs.sign, jobs['verify-signed']]) {
    expect(job.if).toContain("github.ref == 'refs/heads/main'")
    expect(job['runs-on']).toBe('ubuntu-22.04')
    const steps = job.steps
    expect(steps.find((step: any) => step.with?.path === 'protected').with.ref).toBe('${{ github.sha }}')
    expect(steps.some((step: any) => step.with?.['artifact-ids'] === '${{ needs.build.outputs.artifact_id }}')).toBe(true)
    expect(steps.some((step: any) => step.with?.['artifact-ids'] === '${{ needs.qualify.outputs.artifact_id }}')).toBe(true)
    const commands = steps.map((step: any) => step.run ?? '').join('\n')
    expect(commands).toContain('protected/scripts/nix_desktop_attestation.py')
    expect(commands).not.toMatch(/(?:apt-get install|dpkg --install|xvfb-run|bun run|cd source|nix build)/)
    expect(commands).not.toMatch(/(?:python3|bash|node)\s+source\//)
  }
  const steps = jobs.sign.steps
  const verified = steps.findIndex((step: any) => step.run?.includes('nix_desktop_attestation.py verify'))
  const authorized = steps.findIndex((step: any) => step.run?.includes('nix_candidate_authorization.py'))
  const attestations = steps.filter((step: any) => step.uses?.startsWith('actions/attest@'))
  expect(attestations).toHaveLength(3)
  expect(authorized).toBeGreaterThan(verified)
  for (const step of attestations) expect(steps.indexOf(step)).toBeGreaterThan(authorized)
  expect(attestations[1].with['predicate-type']).toBe('https://spdx.dev/Document/v2.3')
  expect(jobs['verify-signed'].steps.some((step: any) => step.run?.includes('--original-qualified original-qualified'))).toBe(true)
  expect(jobs['verify-signed'].steps.some((step: any) => step.run?.includes('--signer-revision "$GITHUB_SHA"'))).toBe(true)
})

test('desktop trust regressions run in the fast contract and trigger Nix validation', () => {
  const tasks = parse(readFileSync('Taskfile.yml', 'utf8')).tasks
  const commands = tasks['ci:test:frontend:core'].cmds
  expect(commands).toContain('bun test scripts/nix_desktop_candidate_contract.test.ts')
  expect(commands).toContain('python3 -m unittest discover -s scripts/tests -p test_nix_desktop_attestation.py')
  const development = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  for (const path of ['scripts/nix_desktop_candidate_contract.test.ts',
    'scripts/nix_desktop_attestation.py', 'scripts/tests/test_nix_desktop_attestation.py']) {
    expect(development.on.pull_request.paths).toContain(path)
  }
})

test('desktop lifecycle isolates execution and uploads only public exact-version evidence', () => {
  const workflow = parse(readFileSync('.github/workflows/nix-desktop-lifecycle.yml', 'utf8'))
  expect(Object.keys(workflow.on)).toEqual(['workflow_dispatch'])
  expect(workflow.permissions).toEqual({ contents: 'read', actions: 'read', attestations: 'read' })
  expect(workflow.concurrency['cancel-in-progress']).toBe(false)
  const job = workflow.jobs.qualify
  expect(job.if).toContain("github.repository == 'flidai/leapview'")
  expect(job.if).toContain("github.ref == 'refs/heads/main'")
  expect(job['runs-on']).toBe('ubuntu-22.04')
  expect(job.environment).toBe('leapview-ephemeral-qualification')
  expect(job.steps[0].with.ref).toBe('${{ github.sha }}')
  const commands = job.steps.map((step: any) => step.run ?? '').join('\n')
  expect(commands).toContain('protected/scripts/nix_desktop_lifecycle_inputs.py')
  expect(commands).toContain('protected/scripts/nix_desktop_lifecycle.py qualify')
  expect(commands).not.toMatch(/nix build|gh workflow run|--no-sandbox|ignore-certificate-errors/)
  expect(job.steps.at(-1).with.path).toBe('${{ runner.temp }}/desktop-lifecycle-public/desktop-lifecycle.json')
  const runtime = readFileSync('scripts/nix_desktop_lifecycle.py', 'utf8')
  expect(runtime).toContain("'--net', '--fork', '--kill-child=SIGKILL'")
  expect(runtime).toContain("'env', '-i'")
  const tasks = parse(readFileSync('Taskfile.yml', 'utf8')).tasks
  expect(tasks['ci:test:frontend:core'].cmds).toContain("python3 -m unittest discover -s scripts/tests -p 'test_nix_desktop_lifecycle*.py'")
})

test('preview publication reuses signed Nix Linux bytes after exact versioned lifecycle qualification', () => {
  const workflow = parse(readFileSync('.github/workflows/desktop-preview-release.yml', 'utf8'))
  for (const field of ['nix_desktop_run_id', 'nix_desktop_run_attempt',
    'nix_desktop_lifecycle_run_id', 'nix_desktop_lifecycle_run_attempt']) {
    expect(workflow.on.workflow_dispatch.inputs[field].required).toBe(true)
  }
  for (const job of Object.values(workflow.jobs) as any[]) {
    expect(job.if).toContain("github.ref == 'refs/heads/main'")
    expect(job.if).toContain("github.repository == 'flidai/leapview'")
  }
  const steps = workflow.jobs.packages.steps
  expect(steps.find((step: any) => step.uses === './.github/actions/desktop-preview-candidate').if)
    .toBe("matrix.artifact != 'linux-x64'")
  const stage = steps.find((step: any) => step.run?.includes('nix_desktop_preview_inputs.py stage'))
  expect(stage.if).toBe("matrix.artifact == 'linux-x64'")
  expect(stage.run).toContain('--lifecycle-run "$LIFECYCLE_RUN" --lifecycle-attempt "$LIFECYCLE_ATTEMPT"')
  expect(stage.run).not.toMatch(/nix build|bun run make/)
  const publish = workflow.jobs.publish.steps.map((step: any) => step.run ?? '').join('\n')
  expect(publish).toContain('node protected/desktop/scripts/verify-release-evidence.mjs')
  expect(publish).not.toContain('node "$candidate/out/evidence/verify-release-evidence.mjs"')
  expect(publish).toContain('--source-digest "$PUBLISHER_SHA" --source-ref refs/heads/main --deny-self-hosted-runners')
})
