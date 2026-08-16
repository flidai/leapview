// Package transport contains capability-neutral HTTP protocol mechanics.
package transport

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultListLimit = 50
	MaxListLimit     = 200
)

func DecodeBody(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body must contain exactly one JSON value")
	}
	return nil
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_ = writeNormalizedJSON(w, value)
}

// writeNormalizedJSON applies the public timestamp representation at the
// transport boundary. Persisted SQLite rows may still contain legacy
// space-separated timestamps; API resources must never expose those values.
func writeNormalizedJSON(w http.ResponseWriter, value any) error {
	encoded, err := CanonicalJSON(value)
	if err != nil {
		return err
	}
	_, err = w.Write(append(encoded, '\n'))
	return err
}

// CanonicalJSON applies the public response normalization and returns the
// exact compact JSON representation used for response ETags.
func CanonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	normalizeTimestampFields(document)
	return json.Marshal(document)
}

func normalizeTimestampFields(value any) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if child == nil && requiredCollectionField(key) {
				current[key] = []any{}
				continue
			}
			if text, ok := child.(string); ok && isTimestampField(key) {
				if parsed, err := parsePublicTimestamp(text); err == nil {
					current[key] = parsed
				}
				continue
			}
			normalizeTimestampFields(child)
		}
	case []any:
		for _, child := range current {
			normalizeTimestampFields(child)
		}
	}
}

func requiredCollectionField(key string) bool {
	switch key {
	case "allowedOrigins", "args", "artifacts", "assets", "authentication", "badges", "bindings", "checks", "columns", "components", "connections", "context", "dashboards", "decisions", "dependencies", "downstream", "edges", "effectiveGrants", "effectiveOrdering", "errors", "facts", "files", "filters", "headers", "items", "kinds", "locations", "managedDataPins", "missingDigests", "operators", "optionDependencies", "pages", "parts", "physicalDependencies", "predicates", "queryFormats", "relationshipPaths", "renderers", "roles", "rows", "sources", "stitchDimensions", "tags", "targets", "uploadProtocols", "upstream", "values", "visuals", "warnings":
		return true
	default:
		return false
	}
}

func isTimestampField(key string) bool {
	return strings.HasSuffix(key, "At") || strings.HasSuffix(key, "Since") || key == "timestamp" || key == "created" || key == "updated"
}

func parsePublicTimestamp(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return value, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC().Format(time.RFC3339Nano), nil
		}
	}
	return value, nil
}

type ProblemFieldError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Field  string `json:"field"`
}

type ProblemDetails struct {
	Code      string              `json:"code"`
	Detail    string              `json:"detail"`
	Errors    []ProblemFieldError `json:"errors"`
	Instance  string              `json:"instance"`
	RequestID string              `json:"requestId"`
	Status    int32               `json:"status"`
	Title     string              `json:"title"`
	Type      string              `json:"type"`
}

func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string, violations []ProblemFieldError) {
	if violations == nil {
		violations = []ProblemFieldError{}
	}
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = w.Header().Get("X-Request-ID")
	}
	if requestID == "" {
		requestID = NewRequestID()
		r.Header.Set("X-Request-ID", requestID)
	}
	w.Header().Set("X-Request-ID", requestID)
	w.Header().Set("Content-Type", "application/problem+json")
	WriteJSON(w, status, ProblemDetails{
		Type: "https://leapview.dev/problems/" + strings.ToLower(code), Title: http.StatusText(status), Status: int32(status),
		Detail: detail, Instance: r.URL.Path, Code: code, RequestID: requestID, Errors: violations,
	})
}

func StrongETag(value string) string {
	sum := sha256.Sum256([]byte(value))
	return strconv.Quote(hex.EncodeToString(sum[:]))
}

func NewRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "req_unavailable"
	}
	return "req_" + hex.EncodeToString(value[:])
}

type keysetPageCursor struct {
	Key string `json:"key"`
}

func KeysetPage[T any](items []T, limitValue *int32, tokenValue *string, key func(T) string) ([]T, *string, error) {
	limit := DefaultListLimit
	if limitValue != nil {
		if *limitValue < 1 || *limitValue > MaxListLimit {
			return nil, nil, fmt.Errorf("limit must be between 1 and 200")
		}
		limit = int(*limitValue)
	}
	start := 0
	if tokenValue != nil && strings.TrimSpace(*tokenValue) != "" {
		cursor, err := decodeKeysetCursor(*tokenValue)
		if err != nil {
			return nil, nil, err
		}
		start = -1
		for index, item := range items {
			if key(item) == cursor.Key {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, nil, fmt.Errorf("cursor key is unavailable")
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	page := append(make([]T, 0, end-start), items[start:end]...)
	var next *string
	if end < len(items) && len(page) > 0 {
		value := encodeKeysetCursor(key(page[len(page)-1]))
		next = &value
	}
	return page, next, nil
}

func encodeKeysetCursor(key string) string {
	payload, _ := json.Marshal(keysetPageCursor{Key: key})
	return "k1." + base64.RawURLEncoding.EncodeToString(payload)
}

func decodeKeysetCursor(value string) (keysetPageCursor, error) {
	if !strings.HasPrefix(value, "k1.") {
		return keysetPageCursor{}, fmt.Errorf("invalid keyset cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "k1."))
	if err != nil {
		return keysetPageCursor{}, fmt.Errorf("invalid keyset cursor")
	}
	var cursor keysetPageCursor
	if json.Unmarshal(raw, &cursor) != nil || cursor.Key == "" {
		return keysetPageCursor{}, fmt.Errorf("invalid keyset cursor")
	}
	return cursor, nil
}
