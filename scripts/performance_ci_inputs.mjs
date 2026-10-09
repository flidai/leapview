import { fingerprintGoBuildInputs } from './go_receipts.mjs'
console.log(JSON.stringify(fingerprintGoBuildInputs(process.cwd(), {
  packages: ['./internal/app', './internal/app/tools/testshard'], tags: ['duckdb_arrow'],
})))
