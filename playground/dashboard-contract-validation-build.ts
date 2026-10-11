import Ajv from 'ajv/dist/2020'
import standaloneCode from 'ajv/dist/standalone'
import { resolve } from 'node:path'
import type schema from '../schemas/json/dashboard-document.schema.json'

type DashboardSchema = typeof schema

/** Derive the historical contract from the same canonical generated schema. */
export function createDashboardValidators(schema: DashboardSchema) {
  // Baseline: 510224992 (2026-10-08). Only the twelve scalar minima differ.
  const baselineSchema = structuredClone(schema)
  for (const name of ['DashboardLayoutDefaults', 'DashboardLayoutOverride', 'DashboardPlacement'] as const) {
    for (const property of Object.values(baselineSchema.$defs[name].properties)) {
      delete (property as { minimum?: number }).minimum
    }
  }
  const ajv = new Ajv({ strict: false, strictNumbers: true, allErrors: true, validateFormats: false, inlineRefs: false, code: { source: true, esm: true } })
  // Distinct IDs also keep this derivation safe if the canonical schema gains an ID.
  ajv.addSchema({ ...schema, $id: 'urn:leapview:playground:dashboard-current' })
  ajv.addSchema({ ...baselineSchema, $id: 'urn:leapview:playground:dashboard-baseline' })
  const current = ajv.getSchema('urn:leapview:playground:dashboard-current')!
  const baseline = ajv.getSchema('urn:leapview:playground:dashboard-baseline')!
  return { ajv, current, baseline }
}

/** Compile on the server; browser loading must never run AJV's schema compiler. */
export function dashboardValidationPlugin(): Bun.BunPlugin {
  return {
    name: 'dashboard-contract-standalone-validation',
    setup(build) {
      build.onLoad({ filter: /[/\\]dashboard-contract-validation\.ts$/ }, async () => {
        // Read on every rebuild so schema edits never reuse an import-cache snapshot.
        const schema = await Bun.file(resolve(import.meta.dir, '../schemas/json/dashboard-document.schema.json')).json() as DashboardSchema
        const { ajv } = createDashboardValidators(schema)
        return {
          contents: standaloneCode(ajv, {
            current: 'urn:leapview:playground:dashboard-current',
            baseline: 'urn:leapview:playground:dashboard-baseline',
          }),
          loader: 'js',
          resolveDir: import.meta.dir,
        }
      })
    },
  }
}
