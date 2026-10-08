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
