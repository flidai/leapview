import { expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { parse } from 'yaml'

const action = parse(readFileSync('.github/actions/setup-ci/action.yml', 'utf8'))
const steps = action.runs.steps
const locked = "inputs.toolchain == 'auto' && runner.os == 'Linux' && runner.arch == 'X64'"
const conventional = "inputs.toolchain == 'conventional' || runner.os != 'Linux' || runner.arch != 'X64'"

test('Linux validation selects locked tools and excludes duplicate installers', () => {
  expect(action.inputs.toolchain.default).toBe('auto')
  const install = steps.find((step: any) => step.name === 'Install locked Nix')
  expect(install.if).toBe(locked)
  expect(install.uses).toMatch(/@[a-f0-9]{40}$/)
  expect(install.with.extra_nix_config).toContain('sandbox = true')
  const environment = steps.find((step: any) => step.name === 'Export locked compiler and browser environment')
  expect(environment.if).toBe(locked)
  expect(environment.run).toBe('nix develop --no-update-lock-file -c python3 scripts/export_nix_ci_environment.py')
  for (const name of ['Set up Go', 'Set up Node.js', 'Set up Bun', 'Install pinned CI tools']) {
    expect(steps.find((step: any) => step.name === name).if).toBe(conventional)
  }
  expect(steps.find((step: any) => step.name === 'Configure bounded tool caches').run)
    .not.toContain('PLAYWRIGHT_BROWSERS_PATH=')
})

test('locked browser validation cannot fall back to runner browser installs', () => {
  const smoke = steps.find((step: any) => step.name === 'Verify locked Chromium and compiler pairing')
  expect(smoke.if).toBe(`inputs.browser == 'true' && ${locked}`)
  expect(smoke.run).toBe('task nix:smoke')
  for (const name of ['Restore Playwright browser cache', 'Install cached Chromium and system dependencies']) {
    expect(steps.find((step: any) => step.name === name).if)
      .toBe(`inputs.browser == 'true' && (${conventional})`)
  }
})

test('all PR, merge and nightly validation jobs use the shared toolchain', () => {
  for (const workflow of ['ci', 'merge-validation', 'nightly']) {
    const config = parse(readFileSync(`.github/workflows/${workflow}.yml`, 'utf8'))
    for (const [name, job] of Object.entries(config.jobs) as [string, any][]) {
      if (!job.steps?.some((step: any) => /\b(go run|task ci:|task generated:check)\b/.test(step.run ?? ''))) continue
      const setup = job.steps.filter((step: any) => step.uses === './.github/actions/setup-ci')
      expect(setup.length, `${workflow}/${name}`).toBe(1)
      expect(setup[0].with?.toolchain).not.toBe('conventional')
    }
  }
})

test('published native binaries retain the conventional builder until artifact qualification', () => {
  const release = parse(readFileSync('.github/workflows/release.yml', 'utf8'))
  for (const job of Object.values(release.jobs) as any[]) {
    for (const step of job.steps ?? []) {
      if (step.uses === './.github/actions/setup-ci') expect(step.with.toolchain).toBe('conventional')
    }
  }
})

test('cache fallbacks retain the toolchain and locked compiler input identity', () => {
  const cache = steps.find((step: any) => step.name === 'Restore candidate Go validation cache')
  expect(cache.uses).toContain('actions/cache/restore@')
  for (const field of ['key', 'restore-keys']) {
    expect(cache.with[field]).toContain('${{ inputs.toolchain }}')
    expect(cache.with[field]).toContain("${{ hashFiles('flake.lock', 'nix/toolchain.nix') }}")
    expect(cache.with[field]).toContain('${{ steps.toolchain.outputs.go-version }}')
  }
})

test('fresh full and nightly qualification provision the deployment Terraform dependency', () => {
  const workflow = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  const development = workflow.jobs.development.steps
  const terraform = development.find((step: any) => step.uses?.startsWith('hashicorp/setup-terraform@'))
  const canonical = steps.find((step: any) => step.name === 'Set up Terraform')
  expect(terraform).toBeDefined()
  expect(terraform.uses).toBe(canonical.uses)
  expect(terraform.with).toEqual(canonical.with)
  expect(terraform.if).toBe("inputs.contract == 'full' || inputs.contract == 'nightly'")
  const contractIndex = development.findIndex((step: any) => step.env?.CONTRACT)
  expect(development.indexOf(terraform)).toBeLessThan(contractIndex)
})
