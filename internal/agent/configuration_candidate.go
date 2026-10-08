package agent

// ConfigurationCandidate retains a non-secret immutable proposal before its
// credential validation. It never becomes active merely by being saved.
type ConfigurationCandidate struct {
	ID               string
	ExpectedRevision int64
	ConfigurationRevision
}
