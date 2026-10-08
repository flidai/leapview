package agent

import "time"

type Limits struct {
	MaxTurns            int
	MaxToolCalls        int
	MaxConcurrentTools  int
	ToolTimeout         time.Duration
	MaxToolResultBytes  int
	MaxToolDisplayBytes int
	// MaxTruncationRetries bounds automatic recovery from incomplete model output.
	// Zero disables recovery. Retries still consume MaxTurns.
	MaxTruncationRetries int

	ContextWindowTokens  int
	ReserveOutputTokens  int
	HardInputLimitTokens int
}

func defaultLimits(l Limits) Limits {
	if l.MaxTurns == 0 {
		l.MaxTurns = 16
	}
	if l.MaxToolCalls == 0 {
		l.MaxToolCalls = 64
	}
	if l.MaxConcurrentTools == 0 {
		l.MaxConcurrentTools = 4
	}
	if l.ToolTimeout == 0 {
		l.ToolTimeout = 30 * time.Second
	}
	if l.MaxToolResultBytes == 0 {
		l.MaxToolResultBytes = 64 * 1024
	}
	if l.MaxToolDisplayBytes == 0 {
		l.MaxToolDisplayBytes = 1024 * 1024
	}
	if l.ContextWindowTokens == 0 {
		l.ContextWindowTokens = 128000
	}
	if l.ReserveOutputTokens == 0 {
		l.ReserveOutputTokens = 4096
	}
	if l.HardInputLimitTokens == 0 {
		l.HardInputLimitTokens = l.ContextWindowTokens - l.ReserveOutputTokens
	}
	return l
}

func validateLimits(l Limits) error {
	if l.MaxTruncationRetries < 0 {
		return NewError(ErrorCodeInvalidArgument, "max truncation retries must not be negative", nil)
	}
	if l.MaxTurns <= 0 {
		return NewError(ErrorCodeInvalidArgument, "max turns must be positive", nil)
	}
	if l.MaxToolCalls <= 0 {
		return NewError(ErrorCodeInvalidArgument, "max tool calls must be positive", nil)
	}
	if l.MaxConcurrentTools <= 0 {
		return NewError(ErrorCodeInvalidArgument, "max concurrent tools must be positive", nil)
	}
	if l.ToolTimeout <= 0 {
		return NewError(ErrorCodeInvalidArgument, "tool timeout must be positive", nil)
	}
	if l.MaxToolResultBytes <= 0 {
		return NewError(ErrorCodeInvalidArgument, "max tool result bytes must be positive", nil)
	}
	if l.MaxToolDisplayBytes <= 0 {
		return NewError(ErrorCodeInvalidArgument, "max tool display bytes must be positive", nil)
	}
	if l.ContextWindowTokens <= 0 {
		return NewError(ErrorCodeInvalidArgument, "context window tokens must be positive", nil)
	}
	if l.ReserveOutputTokens < 0 {
		return NewError(ErrorCodeInvalidArgument, "reserve output tokens must not be negative", nil)
	}
	if l.HardInputLimitTokens <= 0 {
		return NewError(ErrorCodeInvalidArgument, "hard input limit tokens must be positive", nil)
	}
	return nil
}

func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return len(text)/4 + 1
}
