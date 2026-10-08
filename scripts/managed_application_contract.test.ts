import { expect, test } from 'bun:test'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, symlinkSync, writeFileSync, readdirSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { parse } from 'yaml'

const workflow = () => parse(readFileSync('.github/workflows/managed-application-candidate.yml', 'utf8'))

test('managed application qualification requires protected main and read-only explicit inputs', () => {
  const value = workflow()
  expect(Object.keys(value.on)).toEqual(['workflow_dispatch'])
  expect(Object.keys(value.on.workflow_dispatch.inputs)).toEqual(['bootstrap_release_run', 'predecessor_run', 'candidate_run'])
  expect(value.permissions).toEqual({ contents: 'read', actions: 'read' })
  expect(value.concurrency['cancel-in-progress']).toBe(false)
  const job = value.jobs.qualify
  expect(job.if).toBe("github.repository == 'flidai/leapview' && github.ref == 'refs/heads/main'")
  expect(job['runs-on']).toBe('ubuntu-24.04')
  expect(job['timeout-minutes']).toBe(120)
  expect(job.steps.find((step: any) => step.uses?.startsWith('actions/checkout@')).with)
    .toEqual({ ref: '${{ github.sha }}', 'fetch-depth': 0, 'persist-credentials': false })
  for (const step of job.steps.filter((step: any) => step.uses)) expect(step.uses).toMatch(/@[0-9a-f]{40}$/)
  const validate = job.steps.find((step: any) => step.id === 'validate')
  for (const input of ['1', '37755570045', '', '0', '-1', ' 7', '1\n2', '1; exit 0', '$(exit 0)']) {
    const result = spawnSync('bash', ['-euo', 'pipefail', '-c', validate.run], {
      encoding: 'utf8', env: { ...process.env, BOOTSTRAP_RELEASE_RUN: input, PREDECESSOR_RUN: '2', CANDIDATE_RUN: '3' },
    })
    expect(result.status === 0).toBe(/^[1-9][0-9]*$/.test(input))
  }
})

test('failed lifecycle runs retain only regular allowlisted public JSON reports', () => {
  const steps = workflow().jobs.qualify.steps
  const collect = steps.find((step: any) => step.id === 'collect')
  const upload = steps.find((step: any) => step.uses?.startsWith('actions/upload-artifact@'))
  expect(collect.if).toBe('always()')
  expect(upload.if).toBe('always()')
  expect(upload.with.path).toBe('${{ runner.temp }}/managed-application-public/')
  const script = collect.run.match(/<<'PY'\n([\s\S]*?)\nPY/)[1]
  const root = mkdtempSync(join(tmpdir(), 'managed-application-workflow-'))
  try {
    const source = join(root, 'managed-application-evidence')
    mkdirSync(source)
    writeFileSync(join(source, 'application.json'), '{"passed":false}\n', { mode: 0o600 })
    writeFileSync(join(source, 'credential.json'), '{"token":"must-not-upload"}\n')
    const execute = () => spawnSync('python3', ['-c', script, root, String(process.getuid!()), String(process.getgid!())], { encoding: 'utf8' })
    const result = execute()
    if (result.status !== 0) throw new Error(result.stderr)
    const output = join(root, 'managed-application-public')
    expect(readdirSync(output)).toEqual(['application.json'])
    expect(JSON.parse(readFileSync(join(output, 'application.json'), 'utf8'))).toEqual({ passed: false })
    rmSync(output, { recursive: true })
    symlinkSync(join(source, 'credential.json'), join(source, 'first-publication.json'))
    expect(execute().status).not.toBe(0)
  } finally { rmSync(root, { recursive: true, force: true }) }
})
