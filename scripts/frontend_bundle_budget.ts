import { dirname, isAbsolute } from 'node:path'
import { applyReviewedFrontendBundleBudgetProposal } from './frontend_bundle_budget_proposal'
import { verifyFrontendBundleFiles } from './frontend_bundle_files'
import {
  currentGitRevision,
  frontendLockfileSha256,
  frontendPackageManager,
  frontendSourceInputDigest,
} from './frontend_bundle_identity'

export const FRONTEND_BUNDLE_POLICY_PATH = '.quality/frontend-bundle-budget.json'
export const FRONTEND_BUNDLE_EVIDENCE_PATH = '.tmp/frontend-bundle-evidence.json'

export type BytePair = {
  rawBytes: number
  gzipBytes: number
}

type BundleMeasurement = BytePair & {
  files: string[]
}

export type FrontendBundleEvidence = {
  version: 1
  generatedBy: 'scripts/build_assets.ts'
  identity: FrontendBundleBuildIdentity
  entries: Record<string, BundleMeasurement>
  aggregate: BundleMeasurement
}

export type FrontendBundleBuildIdentity = {
  commit: string | null
  commitSource: 'git' | 'build-arg' | 'unavailable'
  sourceInputDigest: string
  bunVersion: string
  nodeVersion: string
  platform: string
  architecture: string
  packageManager: string
  lockfilePath: string
  lockfileSha256: string
}

export type BundleBudget = {
  baseline: BytePair
  max: BytePair
  maxIncreasePercent: BytePair
}

export type FrontendBundleBudgetPolicy = {
  version: 1
  metadata: {
    artifact: string
    generator: 'scripts/build_assets.ts'
    evidencePath: string
    calibration: {
    baseCommit: string
    sourceInputDigest: string
    harnessInputDigest: string
      capturedAt: string
      environment: {
        host: string
        os: string
        kernel: string
        platform: string
        architecture: string
        cpu: {
          model: string
          logicalCPUs: number
          sockets: number
          coresPerSocket: number
          threadsPerCore: number
        }
      }
      toolchain: {
        bunVersion: string
        nodeVersion: string
        packageManager: string
        lockfilePath: string
        lockfileSha256: string
      }
      method: string
      buildCommand: string
      cleanBuilds: number
      measurements: Array<{
        entries: Record<string, BytePair>
        aggregate: BytePair
      }>
    }
  }
  budgets: {
    entries: Record<string, BundleBudget>
    aggregate: BundleBudget
  }
}

type JsonRecord = Record<string, unknown>

const POLICY_TOP_LEVEL_KEYS = ['version', 'metadata', 'budgets']
const EVIDENCE_TOP_LEVEL_KEYS = ['version', 'generatedBy', 'identity', 'entries', 'aggregate']
const MEASUREMENT_KEYS = ['entries', 'aggregate']
const BUDGET_KEYS = ['baseline', 'max', 'maxIncreasePercent']
const BYTE_KEYS = ['rawBytes', 'gzipBytes']
const ENTRY_MEASUREMENT_KEYS = ['files', 'rawBytes', 'gzipBytes']

function isRecord(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function fail(path: string, message: string): never {
  throw new Error(`frontend bundle budget: ${path}: ${message}`)
}

function requireRecord(value: unknown, path: string): JsonRecord {
  if (!isRecord(value)) fail(path, 'expected an object')
  return value
}

function requireKeys(value: JsonRecord, keys: string[], path: string): void {
  const expected = new Set(keys)
  for (const key of Object.keys(value)) {
    if (!expected.has(key)) fail(`${path}.${key}`, 'unexpected field')
  }
  for (const key of keys) {
    if (!(key in value)) fail(`${path}.${key}`, 'missing required field')
  }
}

function requireString(value: unknown, path: string): string {
  if (typeof value !== 'string' || value.length === 0) fail(path, 'expected a non-empty string')
  return value
}

function requireInteger(value: unknown, path: string, minimum = 0): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < minimum) {
    fail(path, `expected a safe integer >= ${minimum}`)
  }
  return value
}

function requirePercentage(value: unknown, path: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) {
    fail(path, 'expected a finite percentage >= 0')
  }
  return value
}

function requireRfc3339(value: unknown, path: string): string {
  const string = requireString(value, path)
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(string) || Number.isNaN(Date.parse(string))) {
    fail(path, 'expected an RFC3339 timestamp')
  }
  return string
}

function parseBuildIdentity(value: unknown, path: string): FrontendBundleBuildIdentity {
  const object = requireRecord(value, path)
  const keys = ['commit', 'commitSource', 'sourceInputDigest', 'bunVersion', 'nodeVersion', 'platform', 'architecture', 'packageManager', 'lockfilePath', 'lockfileSha256']
  requireKeys(object, keys, path)
  if (object.commit !== null && (typeof object.commit !== 'string' || !/^[0-9a-f]{40}$/.test(object.commit))) {
    fail(`${path}.commit`, 'expected null or a 40-character lowercase commit SHA')
  }
  if (object.commitSource !== 'git' && object.commitSource !== 'build-arg' && object.commitSource !== 'unavailable') {
    fail(`${path}.commitSource`, 'expected git, build-arg, or unavailable')
  }
  if ((object.commitSource === 'unavailable') !== (object.commit === null)) {
    fail(`${path}.commitSource`, 'must be unavailable exactly when commit is null')
  }
  for (const key of ['sourceInputDigest', 'lockfileSha256']) {
    if (typeof object[key] !== 'string' || !/^[0-9a-f]{64}$/.test(object[key])) {
      fail(`${path}.${key}`, 'expected a 64-character lowercase SHA-256 digest')
    }
  }
  return {
    commit: object.commit as string | null,
    commitSource: object.commitSource,
    sourceInputDigest: object.sourceInputDigest as string,
    bunVersion: requireString(object.bunVersion, `${path}.bunVersion`),
    nodeVersion: requireString(object.nodeVersion, `${path}.nodeVersion`),
    platform: requireString(object.platform, `${path}.platform`),
    architecture: requireString(object.architecture, `${path}.architecture`),
    packageManager: requireString(object.packageManager, `${path}.packageManager`),
    lockfilePath: requireString(object.lockfilePath, `${path}.lockfilePath`),
    lockfileSha256: object.lockfileSha256 as string,
  }
}

function parseBytePair(value: unknown, path: string, allowPercentage = false): BytePair {
  const object = requireRecord(value, path)
  requireKeys(object, BYTE_KEYS, path)
  return {
    rawBytes: allowPercentage
      ? requirePercentage(object.rawBytes, `${path}.rawBytes`)
      : requireInteger(object.rawBytes, `${path}.rawBytes`),
    gzipBytes: allowPercentage
      ? requirePercentage(object.gzipBytes, `${path}.gzipBytes`)
      : requireInteger(object.gzipBytes, `${path}.gzipBytes`),
  }
}

function parseBudget(value: unknown, path: string): BundleBudget {
  const object = requireRecord(value, path)
  requireKeys(object, BUDGET_KEYS, path)
  const budget = {
    baseline: parseBytePair(object.baseline, `${path}.baseline`),
    max: parseBytePair(object.max, `${path}.max`),
    maxIncreasePercent: parseBytePair(object.maxIncreasePercent, `${path}.maxIncreasePercent`, true),
  }
  for (const metric of ['rawBytes', 'gzipBytes'] as const) {
    if (budget.max[metric] < budget.baseline[metric]) {
      fail(`${path}.max.${metric}`, 'must be greater than or equal to the baseline')
    }
  }
  return budget
}

function parsePathList(value: unknown, path: string): string[] {
  if (!Array.isArray(value) || value.length === 0) fail(path, 'expected a non-empty sorted path array')
  const paths = value.map((item, index) => requireString(item, `${path}[${index}]`))
  const sorted = [...paths].sort()
  if (new Set(paths).size !== paths.length) fail(path, 'contains duplicate paths')
  if (JSON.stringify(paths) !== JSON.stringify(sorted)) fail(path, 'must be sorted lexicographically')
  for (const assetPath of paths) {
    const segments = assetPath.split('/')
    if (isAbsolute(assetPath) || assetPath.includes('\\') || segments.some((segment) => segment === '' || segment === '.' || segment === '..')) {
      fail(`${path}[${paths.indexOf(assetPath)}]`, 'must be a normalized repository-relative path')
    }
  }
  return paths
}

function parseMeasurement(value: unknown, path: string, includeFiles: boolean): BundleMeasurement | BytePair {
  const object = requireRecord(value, path)
  if (includeFiles) {
    requireKeys(object, ENTRY_MEASUREMENT_KEYS, path)
    return {
      files: parsePathList(object.files, `${path}.files`),
      rawBytes: requireInteger(object.rawBytes, `${path}.rawBytes`),
      gzipBytes: requireInteger(object.gzipBytes, `${path}.gzipBytes`),
    }
  }
  requireKeys(object, BYTE_KEYS, path)
  return parseBytePair(object, path)
}

function parseEntries(value: unknown, path: string, includeFiles: boolean): Record<string, BundleMeasurement | BytePair> {
  const object = requireRecord(value, path)
  const entries: Record<string, BundleMeasurement | BytePair> = {}
  for (const [name, measurement] of Object.entries(object)) {
    if (!/^[a-z0-9][a-z0-9-]*$/.test(name)) fail(`${path}.${name}`, 'invalid logical entry name')
    entries[name] = parseMeasurement(measurement, `${path}.${name}`, includeFiles)
  }
  if (Object.keys(entries).length === 0) fail(path, 'must contain at least one logical entry')
  return entries
}

function parseCalibration(value: unknown, path: string): FrontendBundleBudgetPolicy['metadata']['calibration'] {
  const object = requireRecord(value, path)
  const keys = ['baseCommit', 'sourceInputDigest', 'harnessInputDigest', 'capturedAt', 'environment', 'toolchain', 'method', 'buildCommand', 'cleanBuilds', 'measurements']
  requireKeys(object, keys, path)
  if (typeof object.baseCommit !== 'string' || !/^[0-9a-f]{40}$/.test(object.baseCommit)) {
    fail(`${path}.baseCommit`, 'expected a 40-character lowercase commit SHA')
  }
  if (typeof object.sourceInputDigest !== 'string' || !/^[0-9a-f]{64}$/.test(object.sourceInputDigest)) {
    fail(`${path}.sourceInputDigest`, 'expected a 64-character lowercase SHA-256 digest')
  }
  if (typeof object.harnessInputDigest !== 'string' || !/^[0-9a-f]{64}$/.test(object.harnessInputDigest)) {
    fail(`${path}.harnessInputDigest`, 'expected a 64-character lowercase SHA-256 digest')
  }
  const environment = requireRecord(object.environment, `${path}.environment`)
  requireKeys(environment, ['host', 'os', 'kernel', 'platform', 'architecture', 'cpu'], `${path}.environment`)
  const cpu = requireRecord(environment.cpu, `${path}.environment.cpu`)
  requireKeys(cpu, ['model', 'logicalCPUs', 'sockets', 'coresPerSocket', 'threadsPerCore'], `${path}.environment.cpu`)
  const toolchain = requireRecord(object.toolchain, `${path}.toolchain`)
  requireKeys(toolchain, ['bunVersion', 'nodeVersion', 'packageManager', 'lockfilePath', 'lockfileSha256'], `${path}.toolchain`)
  if (!/^[0-9a-f]{64}$/.test(requireString(toolchain.lockfileSha256, `${path}.toolchain.lockfileSha256`))) {
    fail(`${path}.toolchain.lockfileSha256`, 'expected a 64-character lowercase SHA-256 digest')
  }
  const cleanBuilds = requireInteger(object.cleanBuilds, `${path}.cleanBuilds`, 1)
  if (!Array.isArray(object.measurements) || object.measurements.length !== cleanBuilds) {
    fail(`${path}.measurements`, `expected exactly ${cleanBuilds} repeated measurements`)
  }
  const measurements = object.measurements.map((measurement, index) => {
    const measurementPath = `${path}.measurements[${index}]`
    const record = requireRecord(measurement, measurementPath)
    requireKeys(record, MEASUREMENT_KEYS, measurementPath)
    const entries = parseEntries(record.entries, `${measurementPath}.entries`, false) as Record<string, BytePair>
    const aggregate = parseMeasurement(record.aggregate, `${measurementPath}.aggregate`, false) as BytePair
    return { entries, aggregate }
  })
  return {
    baseCommit: object.baseCommit,
    sourceInputDigest: object.sourceInputDigest,
    harnessInputDigest: object.harnessInputDigest,
    capturedAt: requireRfc3339(object.capturedAt, `${path}.capturedAt`),
    environment: {
      host: requireString(environment.host, `${path}.environment.host`),
      os: requireString(environment.os, `${path}.environment.os`),
      kernel: requireString(environment.kernel, `${path}.environment.kernel`),
      platform: requireString(environment.platform, `${path}.environment.platform`),
      architecture: requireString(environment.architecture, `${path}.environment.architecture`),
      cpu: {
        model: requireString(cpu.model, `${path}.environment.cpu.model`),
        logicalCPUs: requireInteger(cpu.logicalCPUs, `${path}.environment.cpu.logicalCPUs`, 1),
        sockets: requireInteger(cpu.sockets, `${path}.environment.cpu.sockets`, 1),
        coresPerSocket: requireInteger(cpu.coresPerSocket, `${path}.environment.cpu.coresPerSocket`, 1),
        threadsPerCore: requireInteger(cpu.threadsPerCore, `${path}.environment.cpu.threadsPerCore`, 1),
      },
    },
    toolchain: {
      bunVersion: requireString(toolchain.bunVersion, `${path}.toolchain.bunVersion`),
      nodeVersion: requireString(toolchain.nodeVersion, `${path}.toolchain.nodeVersion`),
      packageManager: requireString(toolchain.packageManager, `${path}.toolchain.packageManager`),
      lockfilePath: requireString(toolchain.lockfilePath, `${path}.toolchain.lockfilePath`),
      lockfileSha256: toolchain.lockfileSha256 as string,
    },
    method: requireString(object.method, `${path}.method`),
    buildCommand: requireString(object.buildCommand, `${path}.buildCommand`),
    cleanBuilds,
    measurements,
  }
}

function assertEvidenceIdentity(policy: FrontendBundleBudgetPolicy, evidence: FrontendBundleEvidence): void {
  const expected = policy.metadata.calibration
  const actual = evidence.identity
  const mismatches: string[] = []
  const currentDigest = frontendSourceInputDigest()
  if (actual.sourceInputDigest !== currentDigest) {
    mismatches.push(`sourceInputDigest ${JSON.stringify(actual.sourceInputDigest)} != current source inputs ${JSON.stringify(currentDigest)}`)
  }
  const currentCommit = currentGitRevision()
  if (currentCommit !== null && actual.commit !== currentCommit) {
    mismatches.push(`commit ${JSON.stringify(actual.commit)} != current checkout ${JSON.stringify(currentCommit)}`)
  }
  if (actual.packageManager !== frontendPackageManager()) {
    mismatches.push(`packageManager ${JSON.stringify(actual.packageManager)} != current package.json ${JSON.stringify(frontendPackageManager())}`)
  }
  if (actual.lockfilePath !== 'bun.lock') {
    mismatches.push(`lockfilePath ${JSON.stringify(actual.lockfilePath)} != current lockfile bun.lock`)
  } else {
    const currentLockfileSha256 = frontendLockfileSha256()
    if (actual.lockfileSha256 !== currentLockfileSha256) {
      mismatches.push(`lockfileSha256 ${JSON.stringify(actual.lockfileSha256)} != current bun.lock ${JSON.stringify(currentLockfileSha256)}`)
    }
  }
  // Raw/gzip byte budgets are portable across target platforms when the
  // evidence was produced by the pinned Bun release and every emitted file
  // is re-read and checked below. Keep platform and architecture in the
  // evidence for auditability, but do not reject an arm64 image solely for
  // differing from the calibration host; a byte difference still fails the
  // unchanged absolute and ratcheting budgets.
  const checks: Array<[string, string, string]> = [
    ['bunVersion', actual.bunVersion, expected.toolchain.bunVersion],
  ]
  for (const [field, actualValue, expectedValue] of checks) {
    if (actualValue !== expectedValue) mismatches.push(`${field} ${JSON.stringify(actualValue)} != calibrated ${JSON.stringify(expectedValue)}`)
  }
  if (actual.bunVersion !== Bun.version) {
    mismatches.push(`bunVersion ${JSON.stringify(actual.bunVersion)} != checker runtime ${JSON.stringify(Bun.version)}`)
  }
  if (mismatches.length > 0) fail('evidence.identity', `does not match the calibrated build identity (${mismatches.join('; ')})`)
}

export function validateFrontendBundlePolicy(value: unknown, path = FRONTEND_BUNDLE_POLICY_PATH): FrontendBundleBudgetPolicy {
  const object = requireRecord(value, path)
  requireKeys(object, POLICY_TOP_LEVEL_KEYS, path)
  if (object.version !== 1) fail(`${path}.version`, 'must be 1')

  const metadata = requireRecord(object.metadata, `${path}.metadata`)
  requireKeys(metadata, ['artifact', 'generator', 'evidencePath', 'calibration'], `${path}.metadata`)
  if (metadata.generator !== 'scripts/build_assets.ts') {
    fail(`${path}.metadata.generator`, 'must identify scripts/build_assets.ts')
  }
  const budgets = requireRecord(object.budgets, `${path}.budgets`)
  requireKeys(budgets, ['entries', 'aggregate'], `${path}.budgets`)
  const entriesObject = requireRecord(budgets.entries, `${path}.budgets.entries`)
  const entries: Record<string, BundleBudget> = {}
  for (const [name, budget] of Object.entries(entriesObject)) {
    if (!/^[a-z0-9][a-z0-9-]*$/.test(name)) fail(`${path}.budgets.entries.${name}`, 'invalid logical entry name')
    entries[name] = parseBudget(budget, `${path}.budgets.entries.${name}`)
  }
  if (Object.keys(entries).length === 0) fail(`${path}.budgets.entries`, 'must contain at least one logical entry')

  return {
    version: 1,
    metadata: {
      artifact: requireString(metadata.artifact, `${path}.metadata.artifact`),
      generator: 'scripts/build_assets.ts',
      evidencePath: requireString(metadata.evidencePath, `${path}.metadata.evidencePath`),
      calibration: parseCalibration(metadata.calibration, `${path}.metadata.calibration`),
    },
    budgets: {
      entries,
      aggregate: parseBudget(budgets.aggregate, `${path}.budgets.aggregate`),
    },
  }
}

export function validateFrontendBundleEvidence(value: unknown, path = FRONTEND_BUNDLE_EVIDENCE_PATH): FrontendBundleEvidence {
  const object = requireRecord(value, path)
  requireKeys(object, EVIDENCE_TOP_LEVEL_KEYS, path)
  if (object.version !== 1) fail(`${path}.version`, 'must be 1')
  if (object.generatedBy !== 'scripts/build_assets.ts') fail(`${path}.generatedBy`, 'must identify scripts/build_assets.ts')
  const identity = parseBuildIdentity(object.identity, `${path}.identity`)
  const entries = parseEntries(object.entries, `${path}.entries`, true) as Record<string, BundleMeasurement>
  const aggregate = parseMeasurement(object.aggregate, `${path}.aggregate`, true) as BundleMeasurement
  return { version: 1, generatedBy: 'scripts/build_assets.ts', identity, entries, aggregate }
}

function sortedKeys(value: Record<string, unknown>): string[] {
  return Object.keys(value).sort()
}

function compareKeySets(actual: string[], expected: string[], path: string): void {
  const missing = expected.filter((key) => !actual.includes(key))
  const unexpected = actual.filter((key) => !expected.includes(key))
  if (missing.length > 0 || unexpected.length > 0) {
    const details = [
      missing.length > 0 ? `missing ${missing.join(', ')}` : '',
      unexpected.length > 0 ? `unexpected ${unexpected.join(', ')}` : '',
    ].filter(Boolean).join('; ')
    fail(path, `logical entry set does not match policy (${details})`)
  }
}

function relativeLimit(baseline: number, increasePercent: number): number {
  return baseline + Math.floor((baseline * increasePercent) / 100)
}

function compareMeasurement(name: string, measured: BytePair, budget: BundleBudget): string[] {
  const violations: string[] = []
  for (const [metric, label] of [['rawBytes', 'raw'], ['gzipBytes', 'gzip']] as const) {
    const value = measured[metric]
    const absoluteLimit = budget.max[metric]
    const ratchetLimit = relativeLimit(budget.baseline[metric], budget.maxIncreasePercent[metric])
    if (value > absoluteLimit) {
      violations.push(`${name} ${label} bytes ${value} exceeds absolute budget ${absoluteLimit}`)
    }
    if (value > ratchetLimit) {
      violations.push(`${name} ${label} bytes ${value} exceeds relative ratchet ${ratchetLimit} (${budget.baseline[metric]} baseline + ${budget.maxIncreasePercent[metric]}%)`)
    }
  }
  return violations
}

function baselineIncreases(name: string, measured: BytePair, budget: BundleBudget): string[] {
  const increases: string[] = []
  for (const [metric, label] of [['rawBytes', 'raw'], ['gzipBytes', 'gzip']] as const) {
    if (measured[metric] > budget.baseline[metric]) {
      increases.push(`${name} ${label} bytes ${measured[metric]} increases the calibrated baseline ${budget.baseline[metric]}`)
    }
  }
  return increases
}

export function compareFrontendBundleEvidence(
  policy: FrontendBundleBudgetPolicy,
  evidence: FrontendBundleEvidence,
): string[] {
  assertEvidenceIdentity(policy, evidence)
  const policyEntries = sortedKeys(policy.budgets.entries)
  const evidenceEntries = sortedKeys(evidence.entries)
  compareKeySets(evidenceEntries, policyEntries, 'evidence.entries')

  const expectedFiles = new Set<string>()
  for (const name of policyEntries) {
    for (const assetPath of evidence.entries[name].files) expectedFiles.add(assetPath)
  }
  const actualAggregateFiles = evidence.aggregate.files
  const expectedAggregateFiles = [...expectedFiles].sort()
  if (JSON.stringify(actualAggregateFiles) !== JSON.stringify(expectedAggregateFiles)) {
    fail('evidence.aggregate.files', `must equal the sorted union of logical-entry files (expected ${expectedAggregateFiles.join(', ')})`)
  }

  const violations: string[] = []
  for (const name of policyEntries) {
    violations.push(...compareMeasurement(name, evidence.entries[name], policy.budgets.entries[name]))
  }
  violations.push(...compareMeasurement('aggregate', evidence.aggregate, policy.budgets.aggregate))
  return violations
}

export function tightenFrontendBundlePolicy(
  policy: FrontendBundleBudgetPolicy,
  evidence: FrontendBundleEvidence,
): FrontendBundleBudgetPolicy {
  const violations = compareFrontendBundleEvidence(policy, evidence)
  for (const name of [...Object.keys(policy.budgets.entries), 'aggregate']) {
    const measured = name === 'aggregate' ? evidence.aggregate : evidence.entries[name]
    const budget = name === 'aggregate' ? policy.budgets.aggregate : policy.budgets.entries[name]
    violations.push(...baselineIncreases(name, measured, budget))
  }
  if (violations.length > 0) {
    throw new Error([
      'frontend bundle budget update refused: measured evidence would increase or exceed an existing budget.',
      ...violations.map((violation) => `- ${violation}`),
      'Use an explicit --propose-increase path with reviewed evidence for an intentional increase.',
    ].join('\n'))
  }

  const measurement = (name: string): BundleBudget => {
    const measured = name === 'aggregate' ? evidence.aggregate : evidence.entries[name]
    const existing = name === 'aggregate' ? policy.budgets.aggregate : policy.budgets.entries[name]
    return {
      baseline: { rawBytes: measured.rawBytes, gzipBytes: measured.gzipBytes },
      max: { rawBytes: measured.rawBytes, gzipBytes: measured.gzipBytes },
      maxIncreasePercent: { ...existing.maxIncreasePercent },
    }
  }
  const entries: Record<string, BundleBudget> = {}
  for (const name of Object.keys(policy.budgets.entries)) entries[name] = measurement(name)
  return {
    ...policy,
    budgets: { entries, aggregate: measurement('aggregate') },
  }
}

async function readJson(path: string): Promise<unknown> {
  const file = Bun.file(path)
  if (!(await file.exists())) throw new Error(`frontend bundle budget: ${path}: evidence or policy file is missing; run the bounded preparation task first`)
  try {
    return JSON.parse(await file.text())
  } catch (error) {
    throw new Error(`frontend bundle budget: ${path}: malformed JSON (${error instanceof Error ? error.message : String(error)})`)
  }
}

async function writeJson(path: string, value: unknown): Promise<void> {
  const directory = dirname(path)
  if (directory !== '.') await Bun.$`mkdir -p ${directory}`.quiet()
  await Bun.write(path, `${JSON.stringify(value, null, 2)}\n`)
}

export async function checkFrontendBundleBudget(
  policyPath = FRONTEND_BUNDLE_POLICY_PATH,
  evidencePath = FRONTEND_BUNDLE_EVIDENCE_PATH,
): Promise<void> {
  const policy = validateFrontendBundlePolicy(await readJson(policyPath), policyPath)
  const evidence = validateFrontendBundleEvidence(await readJson(evidencePath), evidencePath)
  await verifyFrontendBundleFiles(evidence, evidencePath)
  const violations = compareFrontendBundleEvidence(policy, evidence)
  if (violations.length > 0) {
    throw new Error([
      'frontend bundle budget exceeded:',
      ...violations.map((violation) => `- ${violation}`),
      'Update the implementation or submit an explicit reviewed increase proposal with the clean-build evidence.',
    ].join('\n'))
  }
}

type CliOptions = {
  mode: 'check' | 'update' | 'propose' | 'apply'
  policyPath: string
  evidencePath: string
  proposalPath?: string
  reason?: string
}

function optionValue(arguments_: string[], index: number, option: string): string {
  const value = arguments_[index + 1]
  if (!value || value.startsWith('--')) throw new Error(`frontend bundle budget: ${option} requires a value`)
  return value
}

function parseCli(arguments_: string[]): CliOptions {
  let mode: CliOptions['mode'] = 'check'
  const options: Omit<CliOptions, 'mode'> = {
    policyPath: FRONTEND_BUNDLE_POLICY_PATH,
    evidencePath: FRONTEND_BUNDLE_EVIDENCE_PATH,
  }
  for (let index = 0; index < arguments_.length; index += 1) {
    const argument = arguments_[index]
    if (argument === '--update' || argument === '--write') mode = 'update'
    else if (argument === '--propose-increase' || argument === '--propose' || argument === '--proposal') {
      mode = 'propose'
      options.proposalPath = optionValue(arguments_, index, argument)
      index += 1
    } else if (argument === '--apply-proposal') {
      mode = 'apply'
      options.proposalPath = optionValue(arguments_, index, argument)
      index += 1
    } else if (argument === '--reason') {
      options.reason = optionValue(arguments_, index, argument)
      index += 1
    } else if (argument === '--policy') {
      options.policyPath = optionValue(arguments_, index, argument)
      index += 1
    } else if (argument === '--evidence') {
      options.evidencePath = optionValue(arguments_, index, argument)
      index += 1
    } else {
      throw new Error(`frontend bundle budget: unexpected argument ${argument}`)
    }
  }
  if ((mode === 'propose' || mode === 'apply') && !options.proposalPath) {
    throw new Error('frontend bundle budget: proposal path is required')
  }
  return { mode, ...options }
}

async function main(arguments_: string[]): Promise<void> {
  const options = parseCli(arguments_)
  const policy = validateFrontendBundlePolicy(await readJson(options.policyPath), options.policyPath)
  const evidence = validateFrontendBundleEvidence(await readJson(options.evidencePath), options.evidencePath)
  await verifyFrontendBundleFiles(evidence, options.evidencePath)

  if (options.mode === 'check') {
    const violations = compareFrontendBundleEvidence(policy, evidence)
    if (violations.length > 0) {
      throw new Error([
        'frontend bundle budget exceeded:',
        ...violations.map((violation) => `- ${violation}`),
        'Update the implementation or submit an explicit reviewed increase proposal with the clean-build evidence.',
      ].join('\n'))
    }
    console.log(`frontend bundle budget passed (${Object.keys(evidence.entries).length} logical entries, aggregate ${evidence.aggregate.rawBytes} raw / ${evidence.aggregate.gzipBytes} gzip bytes)`)
    return
  }

  if (options.mode === 'update') {
    const updated = tightenFrontendBundlePolicy(policy, evidence)
    await writeJson(options.policyPath, updated)
    console.log(`frontend bundle budget tightened from ${options.policyPath}`)
    return
  }

  if (options.mode === 'propose') {
    if (!options.reason) throw new Error('frontend bundle budget: --reason is required for an increase proposal')
    const requested = {
      entries: Object.fromEntries(Object.entries(evidence.entries).map(([name, measurement]) => [name, {
        baseline: { rawBytes: measurement.rawBytes, gzipBytes: measurement.gzipBytes },
        max: { rawBytes: measurement.rawBytes, gzipBytes: measurement.gzipBytes },
        maxIncreasePercent: policy.budgets.entries[name]?.maxIncreasePercent ?? { rawBytes: 0, gzipBytes: 0 },
      }])),
      aggregate: {
        baseline: { rawBytes: evidence.aggregate.rawBytes, gzipBytes: evidence.aggregate.gzipBytes },
        max: { rawBytes: evidence.aggregate.rawBytes, gzipBytes: evidence.aggregate.gzipBytes },
        maxIncreasePercent: policy.budgets.aggregate.maxIncreasePercent,
      },
    }
    await writeJson(options.proposalPath!, {
      version: 1,
      kind: 'frontend-bundle-budget-increase-proposal',
      reason: options.reason,
      evidence,
      requestedBudgets: requested,
      review: { approved: false, reviewer: null, reviewedAt: null },
    })
    console.log(`frontend bundle budget increase proposal written to ${options.proposalPath}; policy was not changed`)
    return
  }

  const proposalValue = await readJson(options.proposalPath!)
  const updatedPolicy = applyReviewedFrontendBundleBudgetProposal(policy, evidence, proposalValue, options.proposalPath!)
  await writeJson(options.policyPath, updatedPolicy)
  console.log(`frontend bundle budget reviewed increase applied from ${options.proposalPath}`)
}

if (import.meta.main) {
  main(process.argv.slice(2)).catch((error) => {
    console.error(error instanceof Error ? error.message : String(error))
    process.exit(1)
  })
}
