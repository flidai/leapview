package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
	protocolgen "github.com/flidai/leapview/internal/platform/http/api/gen"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
)

type TransportErrorResponder struct {
	Logger *slog.Logger
}

func (responder TransportErrorResponder) RespondTransportError(ctx context.Context, w http.ResponseWriter, r *http.Request, failure apigenapi.GenTransportError) {
	apitransport.WriteAPIGenFailure(ctx, w, r, responder.Logger, apitransport.APIGenFailure{
		OperationID: failure.OperationID, Kind: failure.Kind, StatusCode: failure.StatusCode,
		Code: failure.Code, PublicDetail: failure.PublicDetail, Cause: failure.Cause,
	})
}

type ResponseBuffer struct {
	downstream http.ResponseWriter
	request    *http.Request
	header     http.Header
	body       bytes.Buffer
	status     int
	committed  bool
}

func NewResponseBuffer(w http.ResponseWriter, r *http.Request) *ResponseBuffer {
	return &ResponseBuffer{downstream: w, request: r, header: http.Header{}}
}

func (w *ResponseBuffer) Header() http.Header { return w.header }

func (w *ResponseBuffer) StatusCode() int {
	if w == nil || w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *ResponseBuffer) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *ResponseBuffer) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.committed {
		return w.downstream.Write(data)
	}
	return w.body.Write(data)
}

// Flush implements http.Flusher for generated SSE handlers. Once a stream
// has been committed, only newly written bytes are sent; previously flushed
// bytes are never replayed.
func (w *ResponseBuffer) Flush() {
	if w.isEventStream() {
		w.flushEventStream()
		return
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	body := w.normalizedBody(status)
	if status >= 200 && status < 300 && strings.HasPrefix(w.header.Get("Content-Type"), "application/json") {
		body = SignResponseCursor(w.request, body)
	}
	if (status == http.StatusCreated || status == http.StatusAccepted) && w.header.Get("Location") == "" {
		if location := responseLocation(w.request, body); location != "" {
			w.header.Set("Location", location)
		}
	}
	if status == http.StatusOK && w.request.Method == http.MethodGet && strings.HasPrefix(w.header.Get("Content-Type"), "application/json") {
		etag := w.header.Get("ETag")
		if etag == "" {
			etag = apitransport.StrongETag(strings.TrimSpace(string(body)))
			w.header.Set("ETag", etag)
		}
		if etagMatches(w.request.Header.Get("If-None-Match"), etag) {
			status = http.StatusNotModified
			body = nil
			w.header.Del("Content-Type")
			w.header.Del("Content-Length")
		}
	}
	if IsQueryRequest(w.request) {
		w.header.Set("Cache-Control", "no-store")
	}
	for key, values := range w.header {
		if strings.EqualFold(key, "X-Request-ID") {
			if len(values) > 0 {
				w.downstream.Header().Set(key, values[len(values)-1])
			}
			continue
		}
		w.downstream.Header().Del(key)
		for _, value := range values {
			w.downstream.Header().Add(key, value)
		}
	}
	w.downstream.WriteHeader(status)
	if len(body) != 0 {
		_, _ = w.downstream.Write(body)
	}
	w.committed = true
}

func (w *ResponseBuffer) isEventStream() bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(w.header.Get("Content-Type"))), "text/event-stream")
}

func (w *ResponseBuffer) flushEventStream() {
	if !w.committed {
		status := w.status
		if status == 0 {
			status = http.StatusOK
		}
		for key, values := range w.header {
			if strings.EqualFold(key, "X-Request-ID") {
				if len(values) > 0 {
					w.downstream.Header().Set(key, values[len(values)-1])
				}
				continue
			}
			w.downstream.Header().Del(key)
			for _, value := range values {
				w.downstream.Header().Add(key, value)
			}
		}
		w.downstream.WriteHeader(status)
		w.committed = true
	}
	if w.body.Len() > 0 {
		_, _ = w.downstream.Write(w.body.Bytes())
		w.body.Reset()
	}
	if flusher, ok := w.downstream.(http.Flusher); ok {
		flusher.Flush()
	}
}

func responseLocation(r *http.Request, body []byte) string {
	if r == nil {
		return ""
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	for _, suffix := range []string{"/cancel", "/finalize"} {
		if strings.HasSuffix(path, suffix) {
			return strings.TrimSuffix(path, suffix)
		}
	}
	var value map[string]any
	if json.Unmarshal(body, &value) != nil {
		return ""
	}
	id, _ := value["id"].(string)
	if id == "" {
		for _, key := range []string{"principal", "apiToken", "clientSecret"} {
			nested, _ := value[key].(map[string]any)
			if candidate, _ := nested["id"].(string); candidate != "" {
				id = candidate
				break
			}
		}
	}
	if id == "" {
		return ""
	}
	return path + "/" + url.PathEscape(id)
}

func (w *ResponseBuffer) normalizedBody(status int) []byte {
	if status < 400 || w.body.Len() == 0 {
		return w.body.Bytes()
	}
	var value map[string]any
	if err := json.Unmarshal(w.body.Bytes(), &value); err != nil {
		return w.body.Bytes()
	}
	if strings.HasPrefix(w.header.Get("Content-Type"), "application/problem+json") {
		if instance, _ := value["instance"].(string); strings.TrimSpace(instance) == "" {
			value["instance"] = w.request.URL.Path
		}
		if requestID, _ := value["requestId"].(string); strings.TrimSpace(requestID) == "" {
			value["requestId"] = w.request.Header.Get("X-Request-ID")
		}
		if errorsValue, present := value["errors"]; !present || errorsValue == nil {
			value["errors"] = []protocolgen.ProblemFieldError{}
		}
		out, err := json.Marshal(value)
		if err != nil {
			return w.body.Bytes()
		}
		return append(out, '\n')
	}
	if _, ok := value["code"]; !ok {
		return w.body.Bytes()
	}
	message, ok := value["message"].(string)
	if !ok {
		return w.body.Bytes()
	}
	requestID := w.request.Header.Get("X-Request-ID")
	code := fmt.Sprintf("HTTP_%d", status)
	if raw, ok := value["code"].(string); ok && raw != "" {
		code = raw
	}
	errors := []protocolgen.ProblemFieldError{}
	if details, ok := value["details"].(map[string]any); ok {
		if problemCode, ok := details["problemCode"].(string); ok && problemCode != "" {
			code = problemCode
		}
		if field, ok := details["field"].(string); ok && field != "" {
			errors = append(errors, protocolgen.ProblemFieldError{Field: field, Code: code, Detail: message})
		}
	}
	problem := protocolgen.ProblemDetails{
		Type:  "https://leapview.dev/problems/" + strings.ToLower(code),
		Title: http.StatusText(status), Status: int32(status), Detail: message,
		Instance: w.request.URL.Path, Code: code, RequestId: requestID, Errors: errors,
	}
	w.header.Set("Content-Type", "application/problem+json")
	out, err := json.Marshal(problem)
	if err != nil {
		return w.body.Bytes()
	}
	return append(out, '\n')
}

func etagMatches(raw, current string) bool {
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "*" || value == current {
			return true
		}
	}
	return false
}
