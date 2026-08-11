// Package client defines the capability-agnostic transport boundary used by
// APIGen's generated typed clients.
package client

import (
	"context"
	"encoding"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
)

// Request is the transport-ready wire request produced by a generated client.
// Body and response decoding remain generic only at this infrastructure
// boundary; generated client methods expose concrete request and response types.
type Request struct {
	OperationID string
	Method      string
	Path        string
	PathParams  map[string]string
	Query       url.Values
	Headers     http.Header
	Body        any
	ContentType string
	Accept      string
}

// Response contains transport metadata shared by every generated operation.
type Response struct {
	StatusCode  int
	Headers     http.Header
	ContentType string
}

// ProblemFieldError is one field-level RFC 9457 extension returned by an API.
type ProblemFieldError struct {
	Field  string `json:"field"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// ProblemDetails is APIGen's transport-neutral structured HTTP failure.
type ProblemDetails struct {
	Type      string              `json:"type"`
	Title     string              `json:"title"`
	Status    int                 `json:"status"`
	Detail    string              `json:"detail"`
	Instance  string              `json:"instance"`
	Code      string              `json:"code"`
	RequestID string              `json:"requestId"`
	Errors    []ProblemFieldError `json:"errors"`
}

// ProblemError preserves a non-success HTTP response and its decoded problem.
type ProblemError struct {
	OperationID string
	Response    Response
	Problem     ProblemDetails
}

func (failure *ProblemError) Error() string {
	if failure == nil {
		return "API problem"
	}
	if failure.Problem.Detail != "" {
		return failure.Problem.Detail
	}
	if failure.Problem.Title != "" {
		return failure.Problem.Title
	}
	return fmt.Sprintf("API problem (HTTP %d)", failure.Response.StatusCode)
}

// UnexpectedProblemError is a valid problem response not declared by the
// generated operation failure vocabulary.
type UnexpectedProblemError struct {
	ProblemError *ProblemError
	Violation    string
}

func (failure *UnexpectedProblemError) Error() string {
	if failure == nil || failure.ProblemError == nil {
		return "unexpected API problem"
	}
	if failure.Violation == "" {
		return "unexpected API problem: " + failure.ProblemError.Error()
	}
	return "unexpected API problem: " + failure.Violation + ": " + failure.ProblemError.Error()
}

func (failure *UnexpectedProblemError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.ProblemError
}

// Transport executes a generated request and decodes a successful response
// into response. A nil response denotes a bodyless success contract.
type Transport interface {
	DoAPIGen(context.Context, Request, any) (Response, error)
}

// FormatValue renders a path parameter using its wire representation.
func FormatValue(value any) string {
	value = indirect(value)
	if value == nil {
		return ""
	}
	if marshaler, ok := value.(encoding.TextMarshaler); ok {
		if encoded, err := marshaler.MarshalText(); err == nil {
			return string(encoded)
		}
	}
	return fmt.Sprint(value)
}

// AddQuery appends a typed query value. Nil pointers are omitted. Slices are
// emitted as repeated values when explode is true and comma-delimited otherwise.
func AddQuery(query url.Values, name string, value any, explode bool) {
	values := wireValues(value)
	if len(values) == 0 {
		return
	}
	if explode {
		for _, item := range values {
			query.Add(name, item)
		}
		return
	}
	query.Add(name, strings.Join(values, ","))
}

// AddHeader appends a typed header value. Nil pointers are omitted and slices
// are emitted as repeated header fields.
func AddHeader(headers http.Header, name string, value any) {
	for _, item := range wireValues(value) {
		headers.Add(name, item)
	}
}

func wireValues(value any) []string {
	value = indirect(value)
	if value == nil {
		return nil
	}
	typed := reflect.ValueOf(value)
	if typed.Kind() != reflect.Array && typed.Kind() != reflect.Slice {
		return []string{FormatValue(value)}
	}
	values := make([]string, 0, typed.Len())
	for index := 0; index < typed.Len(); index++ {
		values = append(values, FormatValue(typed.Index(index).Interface()))
	}
	return values
}

func indirect(value any) any {
	if value == nil {
		return nil
	}
	typed := reflect.ValueOf(value)
	for typed.IsValid() && (typed.Kind() == reflect.Pointer || typed.Kind() == reflect.Interface) {
		if typed.IsNil() {
			return nil
		}
		typed = typed.Elem()
	}
	if !typed.IsValid() {
		return nil
	}
	return typed.Interface()
}
