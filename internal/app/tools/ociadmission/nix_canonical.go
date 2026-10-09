package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf16"
)

// Match the protected Python verifier's sorted, ASCII JSON identity encoding.
// Go's default HTML escaping is deliberately disabled; non-ASCII code points
// use JSON UTF-16 escapes, including surrogate pairs, as Python does.
func nixCanonicalJSON(value any) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	var result bytes.Buffer
	for _, r := range string(bytes.TrimSuffix(encoded.Bytes(), []byte("\n"))) {
		if r <= 127 {
			result.WriteByte(byte(r))
			continue
		}
		if r <= 0xffff {
			fmt.Fprintf(&result, "\\u%04x", r)
			continue
		}
		a, b := utf16.EncodeRune(r)
		fmt.Fprintf(&result, "\\u%04x\\u%04x", a, b)
	}
	return result.Bytes(), nil
}
