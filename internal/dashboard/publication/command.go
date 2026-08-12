package publication

// CommandInvocation carries transport identity into a dashboard publication
// command without exposing APIGen's generic target-map assembly to callers.
type CommandInvocation struct {
	OperationID    string
	Surface        string
	IdempotencyKey string
	RequestID      string
	CorrelationID  string
}
