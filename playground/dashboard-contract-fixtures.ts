import Ajv from 'ajv/dist/2020'
import { parse } from 'yaml'
import schema from '../schemas/json/dashboard-document.schema.json'
import evidence from './dashboard-contract-evidence.json'

// Baseline: 510224992 (2026-10-08). Only the twelve scalar minima differ.
// Reconstruct that historical structural schema from the generated current
// schema, avoiding a second maintained copy of the Dashboard contract.
const baselineSchema = structuredClone(schema)
for (const name of ['DashboardLayoutDefaults', 'DashboardLayoutOverride', 'DashboardPlacement'] as const) {
  for (const property of Object.values(baselineSchema.$defs[name].properties)) {
    delete (property as { minimum?: number }).minimum
  }
}
const ajv = new Ajv({ strict: false, allErrors: true, validateFormats: false })
const current = ajv.compile(schema)
const baseline = ajv.compile(baselineSchema)

export interface DashboardInspection { valid: boolean; issues: { path: string; message: string }[] }

/** Structural inspection only; semantic references and layout interactions need the Go compiler. */
export function inspectDashboard(source: string, useBaseline: boolean): DashboardInspection {
  try {
    const value: unknown = parse(source)
    const validate = useBaseline ? baseline : current
    if (validate(value)) return { valid: true, issues: [] }
    const errors = validate.errors || []
    // Prioritize useful scalar diagnostics without concealing independent
    // errors. oneOf may also report rejected alternative variants.
    const relevant = [...errors.filter(error => error.keyword === 'minimum'), ...errors.filter(error => error.keyword !== 'minimum')]
    const issues = relevant.map(error => ({ path: error.instancePath || '/', message: error.message || 'Invalid value' }))
    return { valid: false, issues: issues.filter((issue, index) => issues.findIndex(other => other.path === issue.path && other.message === issue.message) === index) }
  } catch (error) {
    return { valid: false, issues: [{ path: '/', message: error instanceof Error ? error.message : 'Invalid YAML' }] }
  }
}

export const beforeYAML = `apiVersion: leapview.dev/v1
kind: Dashboard
metadata:
  id: dashboard:executive-sales
  name: executive-sales
  displayName: Executive Sales
  description: Revenue and order trends for sales leadership.
  tags: [sales, revenue]
spec:
  semanticModel: sales
  layout:
    columns: 12
    rowHeight: 48
    gap: 16
    padding: 16
  filters: []
  visuals:
    - id: revenue-by-month
      title: Revenue by month
      type: area
      query:
        type: aggregate
        dimensions: [purchase_month]
        metrics: [revenue]
        sort:
          - field: purchase_month
            direction: asc
        limit: 30
      presentation:
        type: cartesian
    - id: total-revenue
      title: Total revenue
      type: kpi
      query:
        type: aggregate
        dimensions: []
        metrics: [revenue]
      presentation:
        type: kpi
        displayUnits: auto
  pages:
    - id: overview
      title: Overview
      components:
        - id: revenue-trend
          type: visual
          visual: revenue-by-month
          placement: {column: 1, row: 1, columnSpan: 12, rowSpan: 8}
        - id: revenue-kpi
          type: visual
          visual: total-revenue
          placement: {column: 1, row: 10, columnSpan: 3, rowSpan: 3}
`

export const afterYAML = `apiVersion: leapview.dev/v1
kind: Dashboard
metadata:
  id: dashboard:executive-sales
  name: executive-sales
  displayName: Executive Sales
  description: Revenue and order trends for sales leadership.
  tags: [sales, revenue]
spec:
  semanticModel: sales
  layout:
    columns: 12
    rowHeight: 48
    gap: 16
    padding: 16
  filters: []
  visuals:
    - id: revenue-by-month
      title: Revenue by month
      type: area
      query:
        type: aggregate
        dimensions:
          - dimension: purchase_date
            grain: month
            alias: purchase_month
        metrics: [revenue]
        sort:
          - field: purchase_month
            direction: asc
        limit: 30
      presentation:
        type: cartesian
    - id: total-revenue
      title: Total revenue
      type: kpi
      query:
        type: aggregate
        dimensions: []
        metrics: [revenue]
      presentation:
        type: kpi
        displayUnits: auto
  pages:
    - id: overview
      title: Overview
      components:
        - id: revenue-trend
          type: visual
          visual: revenue-by-month
          placement: {column: 1, row: 1, columnSpan: 12, rowSpan: 8}
        - id: revenue-kpi
          type: visual
          visual: total-revenue
          placement: {column: 1, row: 10, columnSpan: 3, rowSpan: 3}
`


/** Evaluation evidence is recorded by the real Go compiler; it is not a public Dashboard schema. */
export interface RecordedCompilerEvidence {
  id: string
  label: string
  source: string
  sourceDigest: string
  sourceRootDigest: string
  valid: boolean
  issues: Array<{ message: string; fieldPath?: string; code?: string; file?: string; line?: number; column?: number }>
  resolvedIntent?: unknown
  resolvedIntentDigest?: string
  bundleDigest?: string
  fragmentPaths?: string[]
}

export const compilerEvidenceMetadata = evidence.metadata
export const dashboardScenarios: readonly RecordedCompilerEvidence[] = evidence.fixtures

export function dashboardScenario(id: string): RecordedCompilerEvidence {
  return dashboardScenarios.find(fixture => fixture.id === id) ?? dashboardScenarios.find(fixture => fixture.id === 'corrected-monthly')!
}

export async function sourceDigest(source: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(source))
  return `sha256:${Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, '0')).join('')}`
}

/** Match bytes within the selected recorded source-root context, never just parsed YAML meaning. */
export function matchingCompilerEvidence(source: string, scenario: string): RecordedCompilerEvidence | undefined {
  const fixture = dashboardScenarios.find(fixture => fixture.id === scenario)
  return fixture?.source === source ? fixture : undefined
}
