import { describe, expect, test } from 'bun:test'
import {
  developmentSessionEventsPath,
  DevelopmentSessionViewController,
  type DevelopmentSessionRecord,
} from './dashboard-page-session'

const identity = (candidateId: string, artifact = 'artifact-a', graph = 'graph-a') => ({
  candidateId, artifactDigest: artifact, graphDigest: graph,
})

describe('development-session stable preview transitions', () => {
  test('an initial A view stays pinned, invalid edits retain A, and a later B advances the stable URL', () => {
    const controller = new DevelopmentSessionViewController()
    const a: DevelopmentSessionRecord = { revision: 1, attempted: identity('candidate-a'), lastValid: identity('candidate-a') }
    expect(controller.consume(a, 'candidate-a')).toMatchObject({ candidateID: 'candidate-a', shouldReload: false, outOfDate: false })

    const invalid: DevelopmentSessionRecord = {
      revision: 2, attempted: identity('', 'artifact-b', 'graph-b'), lastValid: identity('candidate-a'),
      diagnostics: [{ code: 'schema.contract', message: 'password=[REDACTED]', path: 'dashboards/orders.yaml', line: 12, column: 4 }],
    }
    const retained = controller.consume(invalid, 'candidate-a')
    expect(retained).toMatchObject({ candidateID: 'candidate-a', shouldReload: false, outOfDate: true })
    expect(retained.diagnostics[0]).toMatchObject({ path: 'dashboards/orders.yaml', line: 12, column: 4 })

    const b = controller.consume({ revision: 3, attempted: identity('candidate-b', 'artifact-b', 'graph-b'), lastValid: identity('candidate-b', 'artifact-b', 'graph-b') }, 'candidate-a')
    expect(b).toMatchObject({ candidateID: 'candidate-b', shouldReload: true, outOfDate: false })
  })

  test('reconnect replay starts from the newest durable revision and does not loop', () => {
    const controller = new DevelopmentSessionViewController()
    const latest = { revision: 9, lastValid: identity('candidate-b', 'artifact-b', 'graph-b') }
    expect(controller.consume(latest, 'candidate-a').shouldReload).toBe(true)
    expect(controller.consume(latest, 'candidate-b')).toMatchObject({ revision: 9, shouldReload: false })
  })

  test('stable document route derives its session event endpoint without changing query state', () => {
    expect(developmentSessionEventsPath('/api/v1/projects/project_1/targets/target_1/development-session/candidate/preview/dashboards/sales')).toBe('/api/v1/projects/project_1/targets/target_1/development-session/events')
    expect(developmentSessionEventsPath('/candidates/candidate-a/dashboards/sales')).toBeNull()
  })
})
