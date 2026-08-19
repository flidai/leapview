package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// DefaultMaxBytes is the largest document accepted by Decode.
	DefaultMaxBytes int64 = 16 << 20
	// DefaultMaxDepth is the largest object/array nesting depth accepted by Decode.
	DefaultMaxDepth = 100
)

var (
	// ErrDuplicateKey indicates that an object repeats a key under the selected comparison mode.
	ErrDuplicateKey = errors.New("strictjson: duplicate object key")
	// ErrTrailingData indicates that non-whitespace data follows the first JSON value.
	ErrTrailingData = errors.New("strictjson: trailing data")
	// ErrSizeLimit indicates that the encoded document exceeds MaxBytes.
	ErrSizeLimit = errors.New("strictjson: size limit exceeded")
	// ErrDepthLimit indicates that object or array nesting exceeds MaxDepth.
	ErrDepthLimit = errors.New("strictjson: depth limit exceeded")
)

// DuplicateKeyMode controls how object keys are compared for duplicates.
type DuplicateKeyMode uint8

const (
	// CaseFoldedKeys rejects keys that differ only by case. It is the default
	// because encoding/json matches struct fields case-insensitively.
	CaseFoldedKeys DuplicateKeyMode = iota
	// CaseSensitiveKeys compares object keys exactly.
	CaseSensitiveKeys
)

// Options configures decoding. Zero-valued limits use the safe defaults.
type Options struct {
	// MaxBytes bounds the encoded document. Zero uses DefaultMaxBytes.
	MaxBytes int64
	// MaxDepth bounds object and array nesting. Zero uses DefaultMaxDepth.
	MaxDepth int
	// DuplicateKeys selects exact or case-folded key comparison.
	DuplicateKeys DuplicateKeyMode
	// AllowUnknownFields disables the default typed-struct unknown-field check.
	AllowUnknownFields bool
}

// DuplicateKeyError identifies an ambiguous object key.
type DuplicateKeyError struct {
	// Key is the later spelling that collided with a key in the same object.
	Key string
}

func (e *DuplicateKeyError) Error() string {
	return fmt.Sprintf("%s %q", ErrDuplicateKey, e.Key)
}

func (e *DuplicateKeyError) Unwrap() error { return ErrDuplicateKey }

// Decode decodes one JSON value using bounded, strict defaults.
func Decode(data []byte, target any) error {
	return DecodeWithOptions(data, target, Options{})
}

// DecodeWithOptions decodes exactly one JSON value from data.
func DecodeWithOptions(data []byte, target any, options Options) error {
	options, err := normalize(options)
	if err != nil {
		return err
	}
	if int64(len(data)) > options.MaxBytes {
		return fmt.Errorf("%w: maximum is %d bytes", ErrSizeLimit, options.MaxBytes)
	}
	return decode(data, target, options)
}

// DecodeReader reads and decodes exactly one bounded JSON value. It does not
// close the reader.
func DecodeReader(reader io.Reader, target any, options Options) error {
	if reader == nil {
		return errors.New("strictjson: reader is nil")
	}
	options, err := normalize(options)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(reader, options.MaxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > options.MaxBytes {
		return fmt.Errorf("%w: maximum is %d bytes", ErrSizeLimit, options.MaxBytes)
	}
	return decode(data, target, options)
}

func normalize(options Options) (Options, error) {
	if options.MaxBytes == 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	if options.MaxDepth == 0 {
		options.MaxDepth = DefaultMaxDepth
	}
	if options.MaxBytes < 0 {
		return Options{}, errors.New("strictjson: max bytes must be positive")
	}
	if options.MaxDepth < 0 {
		return Options{}, errors.New("strictjson: max depth must be positive")
	}
	if options.DuplicateKeys != CaseFoldedKeys && options.DuplicateKeys != CaseSensitiveKeys {
		return Options{}, errors.New("strictjson: invalid duplicate key mode")
	}
	return options, nil
}

func decode(data []byte, target any, options Options) error {
	if err := validate(data, options); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if !options.AllowUnknownFields {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ErrTrailingData
		}
		return fmt.Errorf("%w: %v", ErrTrailingData, err)
	}
	return nil
}

func validate(data []byte, options Options) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkValue(decoder, 0, options); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return ErrTrailingData
		}
		return fmt.Errorf("%w: %v", ErrTrailingData, err)
	}
	return nil
}

func walkValue(decoder *json.Decoder, depth int, options Options) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	depth++
	if depth > options.MaxDepth {
		return fmt.Errorf("%w: maximum is %d", ErrDepthLimit, options.MaxDepth)
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("strictjson: object key is not a string")
			}
			comparisonKey := key
			if options.DuplicateKeys == CaseFoldedKeys {
				comparisonKey = strings.ToLower(key)
			}
			if _, exists := seen[comparisonKey]; exists {
				return &DuplicateKeyError{Key: key}
			}
			seen[comparisonKey] = struct{}{}
			if err := walkValue(decoder, depth, options); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("strictjson: object ended with %v", end)
		}
	case '[':
		for decoder.More() {
			if err := walkValue(decoder, depth, options); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("strictjson: array ended with %v", end)
		}
	default:
		return fmt.Errorf("strictjson: unexpected delimiter %q", delimiter)
	}
	return nil
}
