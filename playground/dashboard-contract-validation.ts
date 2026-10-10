import schema from '../schemas/json/dashboard-document.schema.json'
import { createDashboardValidators } from './dashboard-contract-validation-build'

// Direct fixture tests compile the canonical schema normally. The Playground
// build substitutes this module with AJV's standalone current/baseline functions.
export const { current, baseline } = createDashboardValidators(schema)
