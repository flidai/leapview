import { fingerprintGoBuildInputs } from './go_receipts.mjs'

const packages = ['./internal/project/compiler', './internal/access/snapshot', './pkg/pagestream',
  './internal/manageddata/runtimeview', './internal/dashboard/http']
console.log(JSON.stringify(fingerprintGoBuildInputs(process.cwd(), { packages, tags: ['duckdb_arrow'] })))
