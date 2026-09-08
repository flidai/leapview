import { afterEach, expect, test } from 'bun:test'
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
import { applyReviewedFrontendBundleBudgetProposal } from './frontend_bundle_budget_proposal'
import { currentGitRevision, frontendCommitIdentity, frontendSourceInputDigest } from './frontend_bundle_identity'

const temporaryPaths: string[] = []
const currentSourceDigest = frontendSourceInputDigest()
const currentCommit = currentGitRevision()

afterEach(async () => {
  await Promise.all(temporaryPaths.splice(0).map((path) => Bun.file(path).delete()))
})

function pair(rawBytes: number, gzipBytes = rawBytes): { rawBytes: number; gzipBytes: number } {
  return { rawBytes, gzipBytes }
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
