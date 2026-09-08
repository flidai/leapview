package observationstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aws/smithy-go"
)

func (s *Store) now() time.Time {
	if s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := s.operationTimeout
	if timeout <= 0 {
		timeout = DefaultOperationTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

func ceilSecond(value time.Time) time.Time {
	value = value.UTC()
	if value.Nanosecond() == 0 {
		return value
	}
	return value.Truncate(time.Second).Add(time.Second)
}

func hashBody(body io.Reader, expectedSize int64) (int64, string, error) {
	if expectedSize < 0 || expectedSize > DefaultMaxSourceBytes {
		return 0, "", fmt.Errorf("%w: body size is outside bounds", ErrInvalid)
	}
	hash := sha256.New()
	limited := io.LimitReader(body, expectedSize+1)
	n, err := io.Copy(hash, limited)
	if err != nil {
		return n, "", err
	}
	if n != expectedSize {
		return n, "", fmt.Errorf("body size mismatch")
	}
	return n, hex.EncodeToString(hash.Sum(nil)), nil
}

func validContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func validateText(value, label string, max int) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || len(value) > max || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%w: %s is invalid", ErrInvalid, label)
	}
	return value, nil
}

func validateBucket(value, label string) (string, error) {
	value, err := validateText(value, label, 255)
	if err != nil || strings.ContainsAny(value, "/\\") {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("%w: %s is invalid", ErrInvalid, label)
	}
	return value, nil
}

func validateEndpoint(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 || !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: endpoint is invalid", ErrInvalid)
	}
	u, err := url.Parse(value)
	if err != nil || value != u.String() || u.Scheme == "" || u.Scheme != strings.ToLower(u.Scheme) || u.Host == "" || u.Host != strings.ToLower(u.Host) || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" {
		return "", fmt.Errorf("%w: endpoint is invalid", ErrInvalid)
	}
	if strings.EqualFold(u.Scheme, "https") {
		return value, nil
	}
	host := u.Hostname()
	if strings.EqualFold(u.Scheme, "http") && (strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return value, nil
	}
	return "", fmt.Errorf("%w: endpoint must use HTTPS or loopback HTTP", ErrInvalid)
}

func ptr[T any](value T) *T { return &value }

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalizedDigest(value []byte) string { return digestBytes(value) }

func rawSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func digest(value string) bool {
	return len(value) == 71 && strings.HasPrefix(value, "sha256:") && rawSHA256(strings.TrimPrefix(value, "sha256:"))
}

func rawDigest(value string) string {
	if strings.HasPrefix(value, "sha256:") {
		return strings.TrimPrefix(value, "sha256:")
	}
	return value
}

func apiCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}

func precondition(err error) bool {
	code := apiCode(err)
	return code == "PreconditionFailed" || code == "ConditionalRequestConflict"
}

func backendError(_ context.Context, operation string, err error) error {
	if apiCode(err) == "NoSuchKey" || apiCode(err) == "NoSuchVersion" || apiCode(err) == "NotFound" {
		return fmt.Errorf("%w: %s", ErrNotFound, operation)
	}
	// Do not include provider error text: AWS errors may contain signed URLs,
	// access keys, or other credential-bearing request details.
	return fmt.Errorf("%w: %s", ErrBackend, operation)
}
