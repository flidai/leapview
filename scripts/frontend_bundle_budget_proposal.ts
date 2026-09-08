import { activeBaselineFromEvidence } from './frontend_bundle_active_baseline'
import {
  type BundleBudget,
  type BytePair,
  type FrontendBundleBudgetPolicy,
  type FrontendBundleEvidence,
  compareFrontendBundleEvidence,
  validateFrontendBundleEvidence,
  validateFrontendBundlePolicy,
} from './frontend_bundle_budget'

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

function rfc3339(value: unknown, path: string): void {
  const text = string(value, path)
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(text) || Number.isNaN(Date.parse(text))) {
    fail(path, 'expected an RFC3339 timestamp')
  }
}

function stableJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(stableJson).join(',')}]`
  if (!isRecord(value)) return JSON.stringify(value)
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJson(value[key])}`).join(',')}}`
}

function exactEntrySet(actual: string[], expected: string[], path: string): void {
  const missing = expected.filter((name) => !actual.includes(name))
  const unexpected = actual.filter((name) => !expected.includes(name))
  if (missing.length || unexpected.length) {
    fail(path, `logical entry set does not match policy (${[
      missing.length ? `missing ${missing.join(', ')}` : '',
      unexpected.length ? `unexpected ${unexpected.join(', ')}` : '',
    ].filter(Boolean).join('; ')})`)
  }
}

export function applyReviewedFrontendBundleBudgetProposal(
  policy: FrontendBundleBudgetPolicy,
  evidence: FrontendBundleEvidence,
  proposalValue: unknown,
  proposalPath = 'proposal',
): FrontendBundleBudgetPolicy {
  validateFrontendBundlePolicy(policy)
  const proposal = record(proposalValue, proposalPath)
  keys(proposal, ['version', 'kind', 'reason', 'evidence', 'requestedBudgets', 'review'], proposalPath)
  if (proposal.version !== 1 || proposal.kind !== 'frontend-bundle-budget-increase-proposal') fail(`${proposalPath}.kind`, 'unsupported proposal format')
  string(proposal.reason, `${proposalPath}.reason`)

  const review = record(proposal.review, `${proposalPath}.review`)
  keys(review, ['approved', 'reviewer', 'reviewedAt'], `${proposalPath}.review`)
  if (review.approved !== true || typeof review.reviewer !== 'string' || review.reviewer.length === 0) {
    fail(`${proposalPath}.review`, 'an explicit reviewer approval and reviewer identity are required')
  }
  rfc3339(review.reviewedAt, `${proposalPath}.review.reviewedAt`)

  const candidate = validateFrontendBundleEvidence(proposal.evidence, `${proposalPath}.evidence`)
  if (stableJson(candidate) !== stableJson(evidence)) fail(`${proposalPath}.evidence`, 'does not match the current candidate evidence')
  // Proposal review may approve size violations, but never stale or malformed
  // evidence from another source tree, toolchain, or logical-entry graph.
  compareFrontendBundleEvidence(policy, candidate)
  const policyEntryNames = Object.keys(policy.budgets.entries).sort()
  exactEntrySet(Object.keys(candidate.entries).sort(), policyEntryNames, `${proposalPath}.evidence.entries`)

  const requestedBudgets = record(proposal.requestedBudgets, `${proposalPath}.requestedBudgets`)
  keys(requestedBudgets, ['entries', 'aggregate'], `${proposalPath}.requestedBudgets`)
  const decision = {
    kind: 'reviewed-increase' as const,
    reason: proposal.reason as string,
    reviewer: review.reviewer as string,
    reviewedAt: review.reviewedAt as string,
  }
  const requestedPolicy = validateFrontendBundlePolicy({
    ...policy,
    metadata: { ...policy.metadata, activeBaseline: activeBaselineFromEvidence(candidate, decision) },
    budgets: requestedBudgets,
  }, `${proposalPath}.requestedBudgets`)
  exactEntrySet(Object.keys(requestedPolicy.budgets.entries).sort(), policyEntryNames, `${proposalPath}.requestedBudgets.entries`)

  const assertCandidateBudget = (name: string, requested: BundleBudget, current: BundleBudget, candidateBytes: BytePair): void => {
    if (stableJson(requested.baseline) !== stableJson(candidateBytes)) fail(`${proposalPath}.requestedBudgets.${name}.baseline`, 'must equal the current candidate evidence')
    if (stableJson(requested.max) !== stableJson(candidateBytes)) fail(`${proposalPath}.requestedBudgets.${name}.max`, 'must equal the current candidate evidence')
    if (stableJson(requested.maxIncreasePercent) !== stableJson(current.maxIncreasePercent)) fail(`${proposalPath}.requestedBudgets.${name}.maxIncreasePercent`, 'must remain unchanged from the current policy')
    for (const metric of ['rawBytes', 'gzipBytes'] as const) {
      if (requested.max[metric] < current.max[metric]) fail(`${proposalPath}.requestedBudgets.${name}.max.${metric}`, 'reviewed proposals may not lower an existing budget')
    }
  }
  for (const name of policyEntryNames) {
    assertCandidateBudget(name, requestedPolicy.budgets.entries[name], policy.budgets.entries[name], {
      rawBytes: evidence.entries[name].rawBytes,
      gzipBytes: evidence.entries[name].gzipBytes,
    })
  }
  assertCandidateBudget('aggregate', requestedPolicy.budgets.aggregate, policy.budgets.aggregate, {
    rawBytes: evidence.aggregate.rawBytes,
    gzipBytes: evidence.aggregate.gzipBytes,
  })
  return {
    ...policy,
    metadata: {
      ...policy.metadata,
      activeBaseline: activeBaselineFromEvidence(candidate, decision),
    },
    budgets: requestedPolicy.budgets,
  }
}
