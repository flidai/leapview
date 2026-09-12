package agent

import (
	"errors"
	"fmt"
)

type ErrorCode string

const (
	ErrorCodeCanceled        ErrorCode = "canceled"
	ErrorCodeBusy            ErrorCode = "busy"
	ErrorCodeInvalidArgument ErrorCode = "invalid_argument"
	ErrorCodeModel           ErrorCode = "model"
	ErrorCodeTool            ErrorCode = "tool"
	ErrorCodeCompaction      ErrorCode = "compaction"
	ErrorCodeLimit           ErrorCode = "limit"
)

var ErrContextLength = errors.New("context length exceeded")

type AgentError struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *AgentError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
}

func (e *AgentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewError(code ErrorCode, message string, err error) error {
	return &AgentError{Code: code, Message: message, Err: err}
}

func IsCode(err error, code ErrorCode) bool {
	var agentErr *AgentError
	return errors.As(err, &agentErr) && agentErr.Code == code
}

func isContextLengthError(err error) bool {
	if errors.Is(err, ErrContextLength) {
		return true
	}
	return IsCode(err, ErrorCodeLimit)
}

type fatalToolError struct {
	err error
}

func (e fatalToolError) Error() string {
	if e.err == nil {
		return "fatal tool error"
	}
	return e.err.Error()
}

func (e fatalToolError) Unwrap() error {
	return e.err
}

func FatalToolError(err error) error {
	return fatalToolError{err: err}
}
