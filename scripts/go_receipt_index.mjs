// Index raw go test -json events; package/cache results never invent named runs.
export function indexGoEvents(text) {
  const packages = new Map(), tests = new Map(), builds = new Map(), errors = []
  const outcomes = { pass: 'passed', fail: 'failed', skip: 'skipped', bench: 'benchmark' }
  const eventTimeRange = { first: null, last: null }
  let eventCount = 0
  const rowFor = (map, key, identity) => {
    if (!map.has(key)) map.set(key, { ...identity, outcome: 'incomplete', runObserved: false, firstTime: null, lastTime: null, elapsed: null })
    return map.get(key)
  }
  for (const [offset, line] of text.split('\n').entries()) {
    if (!line.trim()) continue
    try {
      const event = JSON.parse(line)
      if (!event || typeof event.Action !== 'string' || !['start', 'run', 'pause', 'cont', 'pass', 'fail', 'skip', 'output', 'bench', 'attr', 'build-output', 'build-fail'].includes(event.Action)) throw new Error('unknown event action')
      if (event.Time !== undefined && (typeof event.Time !== 'string' || !Number.isFinite(Date.parse(event.Time)))) throw new Error('invalid event timestamp')
      if (event.Elapsed !== undefined && (!Number.isFinite(event.Elapsed) || event.Elapsed < 0)) throw new Error('invalid elapsed time')
      if (event.Output !== undefined && typeof event.Output !== 'string') throw new Error('invalid output')
      let row
      if (event.Action.startsWith('build-')) {
        if (typeof event.ImportPath !== 'string' || !event.ImportPath) throw new Error('missing build import path')
        row = rowFor(builds, event.ImportPath, { importPath: event.ImportPath })
        if (event.Action === 'build-fail') row.outcome = 'failed'
      } else {
        if (typeof event.Package !== 'string' || !event.Package) throw new Error('missing package')
        const pkg = rowFor(packages, event.Package, { package: event.Package, cached: false, failedBuild: null })
        row = pkg
        if (event.Test !== undefined) {
          if (typeof event.Test !== 'string' || !event.Test) throw new Error('invalid test identity')
          row = rowFor(tests, `${event.Package}\0${event.Test}`, { package: event.Package, test: event.Test })
        } else if (event.Action === 'output' && /^ok\s+\S+\s+.*\(cached\)\s*$/m.test(event.Output ?? '')) pkg.cached = true
        if (event.FailedBuild) pkg.failedBuild = event.FailedBuild
        if (event.Action === 'run') {
          if (row.runObserved || row.outcome !== 'incomplete') throw new Error('duplicate named execution')
          row.runObserved = true
        }
        if (outcomes[event.Action]) {
          if (row.outcome !== 'incomplete') throw new Error('duplicate terminal outcome')
          row.outcome = outcomes[event.Action]
        }
      }
      if (event.Time) { row.firstTime ??= event.Time; row.lastTime = event.Time }
      if (event.Time) {
        if (eventTimeRange.first === null || Date.parse(event.Time) < Date.parse(eventTimeRange.first)) eventTimeRange.first = event.Time
        if (eventTimeRange.last === null || Date.parse(event.Time) > Date.parse(eventTimeRange.last)) eventTimeRange.last = event.Time
      }
      if (event.Elapsed !== undefined) row.elapsed = event.Elapsed
      eventCount++
    } catch (error) { errors.push({ line: offset + 1, reason: error.message }) }
  }
  const sorted = map => [...map.values()].sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b), 'en'))
  return { eventCount, eventTimeRange, errors, packages: sorted(packages), builds: sorted(builds), tests: sorted(tests).map(row => {
    const pkg = packages.get(row.package)
    return { ...row, cached: pkg.cached, packageOutcome: pkg.outcome,
      fresh: errors.length === 0 && row.runObserved && !pkg.cached && pkg.outcome !== 'incomplete' && ['passed', 'failed', 'skipped'].includes(row.outcome) }
  }) }
}
