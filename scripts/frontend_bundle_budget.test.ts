import { afterEach, expect, test } from 'bun:test'
import { execFileSync, spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import {
  checkFrontendBundleBudget,
  compareFrontendBundleEvidence,
  tightenFrontendBundlePolicy,
  validateFrontendBundleEvidence,
  type FrontendBundleBudgetPolicy,
  type FrontendBundleEvidence,
  validateFrontendBundlePolicy,
} from './frontend_bundle_budget'
import { verifyFrontendBundleStaticCoverage } from './frontend_bundle_files'
import { frontendBundleEvidenceSha256 } from './frontend_bundle_active_baseline'
import { applyReviewedFrontendBundleBudgetProposal } from './frontend_bundle_budget_proposal'
import {
  assertFrontendBundleWriterProvenance,
  currentGitRevision,
  frontendCommitIdentity,
  frontendSourceInputDigest,
} from './frontend_bundle_identity'

const temporaryPaths: string[] = []
const currentSourceDigest = frontendSourceInputDigest()
const currentCommit = currentGitRevision()
const writerModes = ['update', 'propose', 'apply'] as const
const repositoryRoot = process.cwd()
const budgetScriptPath = join(import.meta.dir, 'frontend_bundle_budget.ts')

afterEach(async () => {
  await Promise.all(temporaryPaths.splice(0).map((path) => Bun.file(path).delete()))
})

function pair(rawBytes: number, gzipBytes = rawBytes): { rawBytes: number; gzipBytes: number } {
  return { rawBytes, gzipBytes }
}

function gitFixture(): { root: string; head: string } {
  const root = mkdtempSync(join(tmpdir(), 'frontend-writer-provenance-'))
  execFileSync('git', ['init', '--quiet'], { cwd: root })
  execFileSync('git', ['config', 'user.email', 'frontend-budget-test@example.invalid'], { cwd: root })
  execFileSync('git', ['config', 'user.name', 'frontend budget test'], { cwd: root })
  writeFileSync(join(root, 'tracked.txt'), 'clean fixture\n')
  execFileSync('git', ['add', 'tracked.txt'], { cwd: root })
  execFileSync('git', ['commit', '--quiet', '-m', 'fixture'], { cwd: root })
  const head = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim()
  return { root, head }
}

function expectWriterModesToReject(root: string, identity: { commit: string | null; commitSource: 'git' | 'build-arg' | 'unavailable' }, environment: Record<string, string | undefined> = {}, message: string): void {
  for (const mode of writerModes) {
    expect(() => assertFrontendBundleWriterProvenance(mode, identity, environment, root)).toThrow(message)
  }
}

function linkedWorktree(): { parent: string; root: string } {
  if (currentCommit === null) throw new Error('test requires a Git checkout')
  const parent = mkdtempSync(join(tmpdir(), 'frontend-cli-worktree-'))
  const root = join(parent, 'checkout')
  execFileSync('git', ['worktree', 'add', '--detach', '--quiet', root, currentCommit], { cwd: repositoryRoot })
  return { parent, root }
}

function removeLinkedWorktree(worktree: { parent: string; root: string }): void {
  try {
    execFileSync('git', ['worktree', 'remove', '--force', worktree.root], { cwd: repositoryRoot, stdio: 'ignore' })
  } finally {
    rmSync(worktree.parent, { recursive: true, force: true })
  }
}

function runBudgetCli(arguments_: string[], cwd: string): { status: number | null; output: string } {
  const environment = { ...process.env }
  delete environment.BUILD_REVISION
  const result = spawnSync(process.execPath, [budgetScriptPath, ...arguments_], {
    cwd,
    env: environment,
    encoding: 'utf8',
  })
  return { status: result.status, output: `${result.stdout ?? ''}${result.stderr ?? ''}` }
}

function cliFixture(worktreeRoot: string): { root: string; policyPath: string; evidencePath: string } {
  const root = mkdtempSync(join(tmpdir(), 'frontend-budget-cli-'))
  const files = ['login-background-loader.js', 'theme.js', 'vendor/datastar-1.0.2.js']
  const measurement = files.reduce((total, file) => {
    const bytes = readFileSync(join(worktreeRoot, 'static', file))
    return {
      files,
      rawBytes: total.rawBytes + bytes.byteLength,
      gzipBytes: total.gzipBytes + Bun.gzipSync(bytes).byteLength,
    }
  }, { files, rawBytes: 0, gzipBytes: 0 })
  const baseEvidence = evidence(measurement)
  const cliEvidence: FrontendBundleEvidence = {
    ...baseEvidence,
    identity: {
      ...baseEvidence.identity,
      sourceInputDigest: frontendSourceInputDigest(worktreeRoot),
    },
  }
  const basePolicy = policy()
  const budget = (bytes: { rawBytes: number; gzipBytes: number }) => ({
    baseline: { rawBytes: bytes.rawBytes, gzipBytes: bytes.gzipBytes },
    max: { rawBytes: bytes.rawBytes, gzipBytes: bytes.gzipBytes },
    maxIncreasePercent: { rawBytes: 5, gzipBytes: 5 },
  })
  const cliPolicy: FrontendBundleBudgetPolicy = {
    ...basePolicy,
    metadata: {
      ...basePolicy.metadata,
      activeBaseline: {
        evidence: cliEvidence,
        evidenceSha256: frontendBundleEvidenceSha256(cliEvidence),
        decision: {
          kind: 'initial',
          reason: 'CLI provenance test fixture; pending GitHub review (not human-approved).',
          reviewer: null,
          reviewedAt: null,
        },
      },
    },
    budgets: {
      entries: { app: budget(cliEvidence.entries.app) },
      aggregate: budget(cliEvidence.aggregate),
    },
  }
  const policyPath = join(root, 'policy.json')
  const evidencePath = join(root, 'evidence.json')
  writeFileSync(policyPath, `${JSON.stringify(cliPolicy, null, 2)}\n`)
  writeFileSync(evidencePath, `${JSON.stringify(cliEvidence, null, 2)}\n`)
  return { root, policyPath, evidencePath }
}

function policy(overrides: Partial<FrontendBundleBudgetPolicy['budgets']['entries']['app']> = {}): FrontendBundleBudgetPolicy {
  const budget = {
    baseline: pair(100),
    max: pair(120),
    maxIncreasePercent: pair(5, 5),
    ...overrides,
  }
  return {
    version: 1,
    metadata: {
      artifact: 'test production bundles',
      generator: 'scripts/build_assets.ts',
      evidencePath: '.tmp/frontend-bundle-evidence.json',
      activeBaseline: (() => {
        const activeEvidence = evidence()
        return {
          evidence: activeEvidence,
          evidenceSha256: frontendBundleEvidenceSha256(activeEvidence),
          decision: {
            kind: 'initial' as const,
            reason: 'Initial test baseline; pending GitHub review (not human-approved).',
            reviewer: null,
            reviewedAt: null,
          },
        }
      })(),
      calibration: {
        baseCommit: 'e704f88068696c9fe1136a51c22b088663976f31',
        sourceInputDigest: 'd69cc8f07e1dcf0bdd26af31e7a39b3cc2fc58ccfad98f9e90cdead3967d3573',
        harnessInputDigest: '017e837d358f80265afbefee82005d7341c0106ef9b7ad6e573a3999330cfb37',
        capturedAt: '2026-09-08T11:20:57+02:00',
        environment: {
          host: 'test-host',
          os: 'test-os',
          kernel: 'test-kernel',
          platform: 'linux',
          architecture: 'x64',
          cpu: { model: 'test-cpu', logicalCPUs: 1, sockets: 1, coresPerSocket: 1, threadsPerCore: 1 },
        },
        toolchain: {
          bunVersion: '1.3.14',
          nodeVersion: 'v24.3.0',
          packageManager: 'bun@1.3.14',
          lockfilePath: 'bun.lock',
          lockfileSha256: 'ef842b88f7e7ab22495d84c69ca4fe76acb1e9248144195a1fe5a472b87638fa',
        },
        method: 'test fixture',
        buildCommand: 'bun run build',
        cleanBuilds: 1,
        measurements: [{ entries: { app: pair(100) }, aggregate: pair(100) }],
      },
    },
    budgets: { entries: { app: budget }, aggregate: budget },
  }
}

function evidence(overrides: Partial<FrontendBundleEvidence['entries']['app']> = {}): FrontendBundleEvidence {
  const measurement = { files: ['app.js'], rawBytes: 100, gzipBytes: 100, ...overrides }
  return {
    version: 1,
    generatedBy: 'scripts/build_assets.ts',
    identity: {
      commit: currentCommit,
      commitSource: currentCommit === null ? 'unavailable' : 'git',
      sourceInputDigest: currentSourceDigest,
      bunVersion: '1.3.14',
      nodeVersion: 'v24.3.0',
      platform: 'linux',
      architecture: 'x64',
      packageManager: 'bun@1.3.14',
      lockfilePath: 'bun.lock',
      lockfileSha256: 'ef842b88f7e7ab22495d84c69ca4fe76acb1e9248144195a1fe5a472b87638fa',
    },
    entries: { app: measurement },
    aggregate: { ...measurement },
  }
}

function increaseProposal(candidate = evidence({ rawBytes: 130, gzipBytes: 130 })) {
  const candidateBudget = (measurement: { rawBytes: number; gzipBytes: number }) => ({
    baseline: pair(measurement.rawBytes, measurement.gzipBytes),
    max: pair(measurement.rawBytes, measurement.gzipBytes),
    maxIncreasePercent: pair(5, 5),
  })
  return {
    version: 1,
    kind: 'frontend-bundle-budget-increase-proposal',
    reason: 'intentional reviewed test increase',
    evidence: candidate,
    requestedBudgets: {
      entries: { app: candidateBudget(candidate.entries.app) },
      aggregate: candidateBudget(candidate.aggregate),
    },
    review: { approved: true, reviewer: 'reviewer@example.test', reviewedAt: '2026-09-08T11:20:57+02:00' },
  }
}

test('accepts a complete exact logical-entry and aggregate measurement', () => {
  expect(compareFrontendBundleEvidence(policy(), evidence())).toEqual([])
})

test('reports actionable absolute and relative oversized diagnostics', () => {
  const violations = compareFrontendBundleEvidence(policy(), evidence({ rawBytes: 121, gzipBytes: 121 }))
  expect(violations).toEqual(expect.arrayContaining([
    'app raw bytes 121 exceeds absolute budget 120',
    'app raw bytes 121 exceeds relative ratchet 105 (100 baseline + 5%)',
    'aggregate gzip bytes 121 exceeds absolute budget 120',
  ]))
})

test('enforces a relative ratchet even when the absolute ceiling is higher', () => {
  const permissiveAbsolute = policy({ max: pair(200) })
  const violations = compareFrontendBundleEvidence(permissiveAbsolute, evidence({ rawBytes: 106, gzipBytes: 100 }))
  expect(violations).toContain('app raw bytes 106 exceeds relative ratchet 105 (100 baseline + 5%)')
})

test('fails closed for missing, malformed, and unexpected evidence data', () => {
  expect(() => validateFrontendBundleEvidence({ version: 1, generatedBy: 'scripts/build_assets.ts', entries: {} }))
    .toThrow('missing required field')
  expect(() => validateFrontendBundleEvidence({
    ...evidence(),
    entries: { app: { files: ['app.js'], rawBytes: '100', gzipBytes: 100 } },
    aggregate: { files: ['app.js'], rawBytes: 100, gzipBytes: 100 },
  })).toThrow('expected a safe integer')
  expect(() => compareFrontendBundleEvidence(policy(), {
    ...evidence(),
    entries: { ...evidence().entries, unexpected: evidence().entries.app },
  })).toThrow('unexpected unexpected')
  expect(() => validateFrontendBundleEvidence({
    ...evidence(),
    identity: { ...evidence().identity, sourceInputDigest: 'not-a-digest' },
  })).toThrow('64-character lowercase SHA-256 digest')
  expect(() => validateFrontendBundleEvidence({
    ...evidence(),
    identity: { ...evidence().identity, commit: null },
  })).toThrow('must be unavailable exactly when commit is null')
  expect(validateFrontendBundleEvidence({
    ...evidence(),
    identity: { ...evidence().identity, commit: null, commitSource: 'unavailable' },
  }).identity.commit).toBeNull()
  expect(() => compareFrontendBundleEvidence(policy(), {
    ...evidence(),
    identity: { ...evidence().identity, bunVersion: '1.4.2' },
  })).toThrow('bunVersion')
  expect(() => compareFrontendBundleEvidence(policy(), {
    ...evidence(),
    identity: { ...evidence().identity, lockfileSha256: '0'.repeat(64) },
  })).toThrow('current bun.lock')
  expect(() => compareFrontendBundleEvidence(policy(), {
    ...evidence(),
    identity: { ...evidence().identity, commit: '0000000000000000000000000000000000000000' },
  })).toThrow('does not match the calibrated build identity')
})

test('rejects malformed BUILD_REVISION instead of silently falling back to Git', async () => {
  const previous = process.env.BUILD_REVISION
  process.env.BUILD_REVISION = 'not-a-revision'
  try {
    await expect(frontendCommitIdentity()).rejects.toThrow('provided BUILD_REVISION')
  } finally {
    if (previous === undefined) delete process.env.BUILD_REVISION
    else process.env.BUILD_REVISION = previous
  }
})

test('writer modes require a clean real Git checkout and matching recorded HEAD', () => {
  const fixture = gitFixture()
  try {
    const identity = { commit: fixture.head, commitSource: 'git' as const }
    for (const mode of writerModes) {
      expect(() => assertFrontendBundleWriterProvenance(mode, identity, {}, fixture.root)).not.toThrow()
    }
  } finally {
    rmSync(fixture.root, { recursive: true, force: true })
  }
})

test('writer modes reject invocation below the Git worktree root', () => {
  const fixture = gitFixture()
  const nested = join(fixture.root, 'nested')
  try {
    mkdirSync(nested)
    expectWriterModesToReject(nested, { commit: fixture.head, commitSource: 'git' }, {}, 'must run from the Git worktree root')
  } finally {
    rmSync(fixture.root, { recursive: true, force: true })
  }
})

for (const dirtyKind of ['staged', 'unstaged', 'untracked'] as const) {
  test(`writer modes reject ${dirtyKind} nonignored changes`, () => {
    const fixture = gitFixture()
    try {
      if (dirtyKind === 'untracked') {
        writeFileSync(join(fixture.root, 'untracked.txt'), 'not ignored\n')
      } else {
        writeFileSync(join(fixture.root, 'tracked.txt'), `${dirtyKind} change\n`)
        if (dirtyKind === 'staged') execFileSync('git', ['add', 'tracked.txt'], { cwd: fixture.root })
      }
      expectWriterModesToReject(fixture.root, { commit: fixture.head, commitSource: 'git' }, {}, 'clean nonignored working tree')
    } finally {
      rmSync(fixture.root, { recursive: true, force: true })
    }
  })
}

test('writer modes reject missing Git metadata and BUILD_REVISION overrides', () => {
  const missingGit = gitFixture()
  try {
    rmSync(join(missingGit.root, '.git'), { recursive: true, force: true })
    expectWriterModesToReject(missingGit.root, { commit: missingGit.head, commitSource: 'git' }, {}, 'clean Git checkout')
  } finally {
    rmSync(missingGit.root, { recursive: true, force: true })
  }

  const buildArg = gitFixture()
  try {
    expectWriterModesToReject(buildArg.root, { commit: buildArg.head, commitSource: 'git' }, { BUILD_REVISION: '0'.repeat(40) }, 'BUILD_REVISION cannot override')
  } finally {
    rmSync(buildArg.root, { recursive: true, force: true })
  }
})

test('writer modes reject a recorded commit that differs from real HEAD', () => {
  const fixture = gitFixture()
  try {
    expectWriterModesToReject(fixture.root, { commit: '0'.repeat(40), commitSource: 'git' }, {}, 'does not match real Git HEAD')
  } finally {
    rmSync(fixture.root, { recursive: true, force: true })
  }
})

test('the CLI guards every writer mode while check remains usable with dirty input', () => {
  const worktree = linkedWorktree()
  const fixture = cliFixture(worktree.root)
  const dirtyPath = join(worktree.root, `frontend-budget-cli-dirty-${process.pid}`)
  const proposalPath = join(fixture.root, 'proposal.json')
  writeFileSync(dirtyPath, 'nonignored fixture output\n')
  writeFileSync(proposalPath, '{}\n')
  try {
    const writerCommands = [
      ['--update'],
      ['--propose-increase', proposalPath, '--reason', 'CLI provenance test'],
      ['--apply-proposal', proposalPath],
    ]
    for (const command of writerCommands) {
      const result = runBudgetCli([...command, '--policy', fixture.policyPath, '--evidence', fixture.evidencePath], worktree.root)
      if (result.status === 0 || !result.output.includes('clean nonignored working tree')) {
        throw new Error(`writer command ${command.join(' ')} unexpectedly completed (exit ${result.status}); CLI output:\n${result.output}`)
      }
    }
    const check = runBudgetCli(['--policy', fixture.policyPath, '--evidence', fixture.evidencePath], worktree.root)
    if (check.status !== 0 || !check.output.includes('frontend bundle budget passed')) {
      throw new Error(`check command unexpectedly failed (exit ${check.status}); CLI output:\n${check.output}`)
    }
  } finally {
    rmSync(dirtyPath, { force: true })
    removeLinkedWorktree(worktree)
    rmSync(fixture.root, { recursive: true, force: true })
  }
})

test('the CLI writer accepts ignored temporary output in a linked worktree', () => {
  const worktree = linkedWorktree()
  const fixture = cliFixture(worktree.root)
  const temporaryDirectory = join(worktree.root, '.tmp')
  const hadTemporaryDirectory = existsSync(temporaryDirectory)
  const ignoredPath = join(temporaryDirectory, `frontend-budget-cli-ignored-${process.pid}`)
  mkdirSync(temporaryDirectory, { recursive: true })
  writeFileSync(ignoredPath, 'ignored fixture output\n')
  try {
    const status = execFileSync('git', ['status', '--porcelain=v1', '--untracked-files=all'], { cwd: worktree.root, encoding: 'utf8' })
    expect(status).toBe('')
    const result = runBudgetCli(['--update', '--policy', fixture.policyPath, '--evidence', fixture.evidencePath], worktree.root)
    if (result.status !== 0) throw new Error(`ignored-output writer unexpectedly failed (exit ${result.status}); CLI output:\n${result.output}`)
  } finally {
    rmSync(ignoredPath, { force: true })
    if (!hadTemporaryDirectory) rmSync(temporaryDirectory, { recursive: true, force: true })
    removeLinkedWorktree(worktree)
    rmSync(fixture.root, { recursive: true, force: true })
  }
})

test('ordinary evidence comparison remains available for the normal check path', () => {
  expect(compareFrontendBundleEvidence(policy(), evidence())).toEqual([])
})

test('independently rejects shipped JavaScript omitted from aggregate coverage', async () => {
  await expect(verifyFrontendBundleStaticCoverage(evidence(), 'test-evidence')).rejects.toThrow('omits shipped JavaScript')
})

test('rejects stale source evidence but permits a changed calibrated revision with smaller bundles', () => {
  expect(() => compareFrontendBundleEvidence(policy(), {
    ...evidence({ rawBytes: 99, gzipBytes: 99 }),
    identity: { ...evidence().identity, sourceInputDigest: '2'.repeat(64) },
  })).toThrow('current source inputs')

  const priorCalibration = policy().metadata.calibration
  const changedRevisionPolicy = {
    ...policy(),
    metadata: {
      ...policy().metadata,
      calibration: { ...priorCalibration, baseCommit: '0'.repeat(40), sourceInputDigest: '1'.repeat(64) },
    },
  }
  expect(compareFrontendBundleEvidence(changedRevisionPolicy, evidence({ rawBytes: 99, gzipBytes: 99 }))).toEqual([])
})

test('includes tsconfig.json in the source input digest', () => {
  const fixture = mkdtempSync(join(tmpdir(), 'frontend-source-digest-'))
  try {
    const fixtureFiles = {
      'package.json': '{}',
      'bun.lock': 'fixture lockfile',
      'tsconfig.json': '{}',
      'static/app.input.css': 'fixture css',
      'static/login-background-loader.js': 'fixture loader',
      'static/theme.js': 'fixture theme',
      'static/vendor/datastar-1.0.2.js': 'fixture runtime',
      'scripts/build_assets.ts': 'fixture build',
      'scripts/frontend_bundle_options.ts': 'fixture build options',
      'scripts/build_maplibre_worker.ts': 'fixture worker',
      'scripts/generate_lucide_icon_catalog.ts': 'fixture icons',
      'scripts/generate_visualization_validator.ts': 'fixture validator',
    }
    for (const [relativePath, contents] of Object.entries(fixtureFiles)) {
      const path = join(fixture, relativePath)
      mkdirSync(dirname(path), { recursive: true })
      writeFileSync(path, contents)
    }
    mkdirSync(join(fixture, 'web'))

    const originalDigest = frontendSourceInputDigest(fixture)
    writeFileSync(join(fixture, 'scripts/frontend_bundle_options.ts'), 'fixture build options changed')
    const optionsDigest = frontendSourceInputDigest(fixture)
    expect(optionsDigest).not.toBe(originalDigest)

    writeFileSync(join(fixture, 'tsconfig.json'), '{"compilerOptions":{"strict":true}}')

    expect(frontendSourceInputDigest(fixture)).not.toBe(optionsDigest)
  } finally {
    rmSync(fixture, { recursive: true, force: true })
  }
})

test('accepts an alternate recorded architecture when exact bytes remain within budget', () => {
  expect(compareFrontendBundleEvidence(policy(), {
    ...evidence(),
    identity: { ...evidence().identity, platform: 'linux', architecture: 'arm64' },
  })).toEqual([])
})

test('requires strict calibration commit, timestamp, environment, and toolchain identity', () => {
  const calibration = policy().metadata.calibration
  expect(() => validateFrontendBundlePolicy({
    ...policy(),
    metadata: { ...policy().metadata, calibration: { ...calibration, baseCommit: 'not-a-commit' } },
  })).toThrow('40-character lowercase commit SHA')
  expect(() => validateFrontendBundlePolicy({
    ...policy(),
    metadata: { ...policy().metadata, calibration: { ...calibration, capturedAt: 'not-rfc3339' } },
  })).toThrow('RFC3339')
  expect(() => validateFrontendBundlePolicy({
    ...policy(),
    metadata: { ...policy().metadata, calibration: { ...calibration, environment: { ...calibration.environment, architecture: '' } } },
  })).toThrow('non-empty string')
  expect(() => validateFrontendBundlePolicy({
    ...policy(),
    metadata: { ...policy().metadata, calibration: { ...calibration, toolchain: { ...calibration.toolchain, lockfileSha256: 'bad' } } },
  })).toThrow('64-character lowercase SHA-256 digest')
  expect(() => validateFrontendBundlePolicy({
    ...policy(),
    budgets: { ...policy().budgets, entries: { app: { ...policy().budgets.entries.app, max: pair(99) } } },
  })).toThrow('greater than or equal to the baseline')
})

test('requires an intact active baseline with attributable evidence identity', () => {
  const parsed = validateFrontendBundlePolicy(policy())
  expect(parsed.metadata.activeBaseline.evidence.identity.commit).toBe(currentCommit)
  expect(parsed.metadata.activeBaseline.evidenceSha256).toBe(frontendBundleEvidenceSha256(parsed.metadata.activeBaseline.evidence))
  expect(() => validateFrontendBundlePolicy({
    ...policy(),
    metadata: { ...policy().metadata, activeBaseline: undefined },
  })).toThrow('metadata.activeBaseline: expected an object')
  expect(() => validateFrontendBundlePolicy({
    ...policy(),
    metadata: {
      ...policy().metadata,
      activeBaseline: { ...policy().metadata.activeBaseline, evidenceSha256: '0'.repeat(64) },
    },
  })).toThrow('does not match canonical embedded evidence')
  const unattributedEvidence = {
    ...policy().metadata.activeBaseline.evidence,
    identity: { ...policy().metadata.activeBaseline.evidence.identity, commit: null, commitSource: 'unavailable' as const },
  }
  expect(() => validateFrontendBundlePolicy({
    ...policy(),
    metadata: {
      ...policy().metadata,
      activeBaseline: {
        ...policy().metadata.activeBaseline,
        evidence: unattributedEvidence,
        evidenceSha256: frontendBundleEvidenceSha256(unattributedEvidence),
      },
    },
  })).toThrow('active baseline requires an attributable commit SHA')
  expect(() => validateFrontendBundlePolicy({
    ...policy(),
    budgets: {
      ...policy().budgets,
      entries: { app: { ...policy().budgets.entries.app, baseline: pair(101) } },
    },
  })).toThrow('does not match policy baseline 101')
  expect(() => validateFrontendBundlePolicy({
    ...policy(),
    budgets: { ...policy().budgets, aggregate: { ...policy().budgets.aggregate, baseline: pair(101) } },
  })).toThrow('evidence.aggregate.rawBytes: does not match policy baseline 101')
})

test('ordinary update refuses a budget increase and tightens only on passing evidence', () => {
  expect(() => tightenFrontendBundlePolicy(policy(), evidence({ rawBytes: 121, gzipBytes: 100 })))
    .toThrow('frontend bundle budget update refused')
  expect(() => tightenFrontendBundlePolicy(policy(), evidence({ rawBytes: 104, gzipBytes: 104 })))
    .toThrow('increases the calibrated baseline')
  const tightened = tightenFrontendBundlePolicy(policy(), evidence({ rawBytes: 99, gzipBytes: 99 }))
  expect(tightened.budgets.entries.app.max).toEqual(pair(99))
  expect(tightened.budgets.aggregate.max).toEqual(pair(99))
})

test('reviewed proposal requires approval, RFC3339 review date, exact entries, and candidate-bound budgets', () => {
  const candidate = evidence({ rawBytes: 130, gzipBytes: 130 })
  const proposal = increaseProposal(candidate)
  expect(applyReviewedFrontendBundleBudgetProposal(policy(), candidate, proposal).budgets.entries.app.max).toEqual(pair(130))

  expect(() => applyReviewedFrontendBundleBudgetProposal(policy(), candidate, {
    ...proposal,
    review: { ...proposal.review, approved: false },
  })).toThrow('explicit reviewer approval')
  expect(() => applyReviewedFrontendBundleBudgetProposal(policy(), candidate, {
    ...proposal,
    review: { ...proposal.review, reviewedAt: 'tomorrow' },
  })).toThrow('RFC3339')
  expect(() => applyReviewedFrontendBundleBudgetProposal(policy(), candidate, {
    ...proposal,
    requestedBudgets: { ...proposal.requestedBudgets, entries: { ...proposal.requestedBudgets.entries, extra: proposal.requestedBudgets.entries.app } },
  })).toThrow('logical entry set does not match policy')
  expect(() => applyReviewedFrontendBundleBudgetProposal(policy(), candidate, {
    ...proposal,
    requestedBudgets: { ...proposal.requestedBudgets, aggregate: { ...proposal.requestedBudgets.aggregate, max: pair(999) } },
  })).toThrow('must equal the current candidate evidence')
  expect(() => applyReviewedFrontendBundleBudgetProposal(policy(), candidate, {
    ...proposal,
    requestedBudgets: { ...proposal.requestedBudgets, aggregate: { ...proposal.requestedBudgets.aggregate, maxIncreasePercent: pair(6, 5) } },
  })).toThrow('must remain unchanged from the current policy')
})

test('tightening and reviewed increases persist active evidence, identity, audit, and reload', () => {
  const tightenedEvidence = evidence({ rawBytes: 99, gzipBytes: 99 })
  const tightened = tightenFrontendBundlePolicy(policy(), tightenedEvidence)
  const reloadedTightening = validateFrontendBundlePolicy(JSON.parse(JSON.stringify(tightened)))
  expect(reloadedTightening.metadata.activeBaseline.evidence).toEqual(tightenedEvidence)
  expect(reloadedTightening.metadata.activeBaseline.decision).toEqual({
    kind: 'tightening',
    reason: 'Current clean-build evidence passed the existing budget and tightened the active baseline.',
    reviewer: null,
    reviewedAt: null,
  })

  const candidate = evidence({ rawBytes: 130, gzipBytes: 130 })
  const applied = applyReviewedFrontendBundleBudgetProposal(policy(), candidate, increaseProposal(candidate))
  const reloadedIncrease = validateFrontendBundlePolicy(JSON.parse(JSON.stringify(applied)))
  expect(reloadedIncrease.metadata.activeBaseline.evidence).toEqual(candidate)
  expect(reloadedIncrease.metadata.activeBaseline.evidenceSha256).toBe(frontendBundleEvidenceSha256(candidate))
  expect(reloadedIncrease.metadata.activeBaseline.decision).toEqual({
    kind: 'reviewed-increase',
    reason: 'intentional reviewed test increase',
    reviewer: 'reviewer@example.test',
    reviewedAt: '2026-09-08T11:20:57+02:00',
  })
})

test('the file-level checker fails closed when preparation did not generate evidence', async () => {
  const suffix = `${Date.now()}-${Math.random().toString(16).slice(2)}`
  const policyPath = `.tmp/frontend-bundle-budget-test-${suffix}-policy.json`
  const evidencePath = `.tmp/frontend-bundle-budget-test-${suffix}-evidence.json`
  temporaryPaths.push(policyPath, evidencePath)
  await Bun.$`mkdir -p .tmp`.quiet()
  await Bun.write(policyPath, `${JSON.stringify(policy())}\n`)
  await expect(checkFrontendBundleBudget(policyPath, evidencePath)).rejects.toThrow('file is missing')
  await Bun.write(evidencePath, '{')
  await expect(checkFrontendBundleBudget(policyPath, evidencePath)).rejects.toThrow('malformed JSON')
  await Bun.write(evidencePath, `${JSON.stringify(evidence())}\n`)
  await expect(checkFrontendBundleBudget(policyPath, evidencePath)).rejects.toThrow('emitted file static/app.js is missing')
})
