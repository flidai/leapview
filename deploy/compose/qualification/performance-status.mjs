export function dashboardRevisionSettled(snapshot, expectedRevision) {
  if (!snapshot || snapshot.filterRevision !== expectedRevision) return false
  const status = snapshot.status
  if (!status || status.generation <= 0 || status.loading || status.error) return false
  const visuals = Object.values(snapshot.visuals || {})
  return visuals.length > 0 && visuals.every((visual) =>
    visual.filterRevision === expectedRevision
    && visual.streamGeneration === status.generation
    && visual.status !== 'loading'
    && visual.status !== 'error')
}
