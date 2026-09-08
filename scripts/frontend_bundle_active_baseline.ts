import { createHash } from 'node:crypto'

import type { BytePair, FrontendBundleEvidence } from './frontend_bundle_budget'

export type FrontendBundleBaselineDecisionKind = 'initial' | 'tightening' | 'reviewed-increase'

export type FrontendBundleBaselineDecision = {
  kind: FrontendBundleBaselineDecisionKind
  reason: string
  reviewer: string | null
  reviewedAt: string | null
}

export type FrontendBundleActiveBaseline = {
  evidence: FrontendBundleEvidence
  evidenceSha256: string
  decision: FrontendBundleBaselineDecision
}

type BaselineBudgets = {
  entries: Record<string, { baseline: BytePair }>
  aggregate: { baseline: BytePair }
}

type EvidenceValidator = (value: unknown, path: string) => FrontendBundleEvidence
type JsonRecord = Record<string, unknown>

function isRecord(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function fail(path: string, message: string): never {
  throw new Error(`frontend bundle budget: ${path}: ${message}`)
}

function record(value: unknown, path: string): JsonRecord {
  if (!isRecord(value)) fail(path, 'expected an object')
  return value
}

function keys(value: JsonRecord, expected: string[], path: string): void {
  const allowed = new Set(expected)
  for (const key of Object.keys(value)) if (!allowed.has(key)) fail(`${path}.${key}`, 'unexpected field')
  for (const key of expected) if (!(key in value)) fail(`${path}.${key}`, 'missing required field')
}

function string(value: unknown, path: string): string {
  if (typeof value !== 'string' || value.length === 0) fail(path, 'expected a non-empty string')
  return value
}

function rfc3339(value: unknown, path: string): string {
  const text = string(value, path)
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(text) || Number.isNaN(Date.parse(text))) {
    fail(path, 'expected an RFC3339 timestamp')
  }
  return text
}

export function canonicalJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(',')}]`
  if (!isRecord(value)) return JSON.stringify(value)
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJson(value[key])}`).join(',')}}`
}

export function frontendBundleEvidenceSha256(evidence: FrontendBundleEvidence): string {
  return createHash('sha256').update(canonicalJson(evidence)).digest('hex')
}

export function activeBaselineFromEvidence(
  evidence: FrontendBundleEvidence,
  decision: FrontendBundleBaselineDecision,
): FrontendBundleActiveBaseline {
  if (evidence.identity.commit === null) {
    throw new Error('frontend bundle budget: active baseline requires an attributable commit SHA')
  }
  return { evidence, evidenceSha256: frontendBundleEvidenceSha256(evidence), decision }
}

function compareNames(actual: string[], expected: string[], path: string): void {
  const missing = expected.filter((name) => !actual.includes(name))
  const unexpected = actual.filter((name) => !expected.includes(name))
  if (missing.length || unexpected.length) {
    fail(path, `logical entry set does not match policy (${[
      missing.length ? `missing ${missing.join(', ')}` : '',
      unexpected.length ? `unexpected ${unexpected.join(', ')}` : '',
    ].filter(Boolean).join('; ')})`)
  }
}

function parseDecision(value: unknown, path: string): FrontendBundleBaselineDecision {
  const object = record(value, path)
  keys(object, ['kind', 'reason', 'reviewer', 'reviewedAt'], path)
  if (object.kind !== 'initial' && object.kind !== 'tightening' && object.kind !== 'reviewed-increase') {
    fail(`${path}.kind`, 'expected initial, tightening, or reviewed-increase')
  }
  const reason = string(object.reason, `${path}.reason`)
  const reviewer = object.reviewer === null ? null : string(object.reviewer, `${path}.reviewer`)
  const reviewedAt = object.reviewedAt === null ? null : rfc3339(object.reviewedAt, `${path}.reviewedAt`)
  if (object.kind === 'reviewed-increase' && (reviewer === null || reviewedAt === null)) {
    fail(path, 'reviewed-increase requires reviewer and reviewedAt')
  }
  if (object.kind !== 'reviewed-increase' && (reviewer !== null || reviewedAt !== null)) {
    fail(path, 'initial and tightening decisions cannot claim local review')
  }
  return { kind: object.kind, reason, reviewer, reviewedAt }
}

export function validateActiveBaseline(
  value: unknown,
  path: string,
  validateEvidence: EvidenceValidator,
  budgets: BaselineBudgets,
): FrontendBundleActiveBaseline {
  const object = record(value, path)
  keys(object, ['evidence', 'evidenceSha256', 'decision'], path)
  const evidence = validateEvidence(object.evidence, `${path}.evidence`)
  if (evidence.identity.commit === null) fail(`${path}.evidence.identity.commit`, 'active baseline requires an attributable commit SHA')
  const evidenceSha256 = string(object.evidenceSha256, `${path}.evidenceSha256`)
  if (!/^[0-9a-f]{64}$/.test(evidenceSha256)) fail(`${path}.evidenceSha256`, 'expected a 64-character lowercase SHA-256 digest')
  if (frontendBundleEvidenceSha256(evidence) !== evidenceSha256) fail(`${path}.evidenceSha256`, 'does not match canonical embedded evidence')
  const decision = parseDecision(object.decision, `${path}.decision`)

  const policyEntries = Object.keys(budgets.entries).sort()
  const evidenceEntries = Object.keys(evidence.entries).sort()
  compareNames(evidenceEntries, policyEntries, `${path}.evidence.entries`)
  for (const name of policyEntries) {
    const expected = budgets.entries[name].baseline
    const actual = evidence.entries[name]
    for (const metric of ['rawBytes', 'gzipBytes'] as const) {
      if (expected[metric] !== actual[metric]) {
        fail(`${path}.evidence.entries.${name}.${metric}`, `does not match policy baseline ${expected[metric]}`)
      }
    }
  }
  for (const metric of ['rawBytes', 'gzipBytes'] as const) {
    if (budgets.aggregate.baseline[metric] !== evidence.aggregate[metric]) {
      fail(`${path}.evidence.aggregate.${metric}`, `does not match policy baseline ${budgets.aggregate.baseline[metric]}`)
    }
  }
  return { evidence, evidenceSha256, decision }
}
