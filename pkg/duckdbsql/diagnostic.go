package duckdbsql

import "fmt"

type ErrorKind uint8

const (
	ErrorSyntax ErrorKind = iota + 1
	ErrorUnsupportedStatement
	ErrorCompatibility
	ErrorMalformed
	ErrorLimit
	ErrorConfiguration
)

func (k ErrorKind) String() string {
	switch k {
	case ErrorSyntax:
		return "syntax"
	case ErrorUnsupportedStatement:
		return "unsupported_statement"
	case ErrorCompatibility:
		return "compatibility"
	case ErrorMalformed:
		return "malformed"
	case ErrorLimit:
		return "limit"
	case ErrorConfiguration:
		return "configuration"
	default:
		return "unknown"
	}
}

// ParseError is the stable error contract for parser and decoder failures.
// DuckDBType and BytePosition preserve engine diagnostics when supplied.
type ParseError struct {
	Kind          ErrorKind
	DuckDBType    string
	DuckDBSubtype string
	Message       string
	BytePosition  int
	Cause         error
}

func (e *ParseError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return fmt.Sprintf("duckdbsql %s error", e.Kind)
	}
	return fmt.Sprintf("duckdbsql %s error: %s", e.Kind, e.Message)
}

func (e *ParseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func syntaxError(message, typ, subtype string, position int) *ParseError {
	if position < 0 {
		position = -1
	}
	return &ParseError{Kind: ErrorSyntax, Message: message, DuckDBType: typ, DuckDBSubtype: subtype, BytePosition: position}
}

func unsupportedError(message, typ string) *ParseError {
	return &ParseError{Kind: ErrorUnsupportedStatement, Message: message, DuckDBType: typ, BytePosition: -1}
}

func compatibilityError(message string) *ParseError {
	return &ParseError{Kind: ErrorCompatibility, Message: message, BytePosition: -1}
}

func malformedError(message string, cause error) *ParseError {
	return &ParseError{Kind: ErrorMalformed, Message: message, BytePosition: -1, Cause: cause}
}

func limitError(message string) *ParseError {
	return &ParseError{Kind: ErrorLimit, Message: message, BytePosition: -1}
}
