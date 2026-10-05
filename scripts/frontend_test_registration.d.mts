export type FrontendRegistrationResult = {
  componentTestCount: number
  reachableTasks: string[]
  reachableTaskInvocations: Array<{ task: string; vars: Record<string, unknown> }>
  reachableScripts: string[]
  missing: string[]
  outsideFrontendGraph: string[]
  missingScripts: string[]
  missingRootTasks: string[]
  shardValues: string[]
}

export function auditFrontendTestRegistration(input: {
  taskfile: Record<string, unknown> & { tasks?: Record<string, unknown> }
  packageJson: { scripts?: Record<string, string> }
  componentTestFiles: string[]
  taskRoots?: string[]
}): FrontendRegistrationResult

export function registrationFailures(result: FrontendRegistrationResult): string[]
export function taskCommandTargets(command: string, knownTaskNames: Set<string>, variables?: Record<string, unknown>): string[]
export function bunRunTestScripts(command: string): string[]
export function testPathPatterns(command: string): string[]
export function globMatches(pattern: string, value: string): boolean
