package workload

// Clone helpers keep observer-facing maps detached from controller state.
func cloneActorStats(source map[string]ActorStats) map[string]ActorStats {
	result := make(map[string]ActorStats, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
func cloneClassStats(source map[Class]ClassStats) map[Class]ClassStats {
	result := make(map[Class]ClassStats, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
