package typednil

import (
	"testing"
	"unsafe"
)

func TestIsNilHandlesEveryNilCapableKindAndScalars(t *testing.T) {
	var nilChannel chan struct{}
	var nilFunction func()
	var nilMap map[string]string
	var nilPointer *int
	var nilSlice []string
	var nilUnsafePointer unsafe.Pointer
	unsafeValue := 0
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{name: "nil", value: nil, want: true},
		{name: "channel", value: nilChannel, want: true},
		{name: "function", value: nilFunction, want: true},
		{name: "map", value: nilMap, want: true},
		{name: "pointer", value: nilPointer, want: true},
		{name: "slice", value: nilSlice, want: true},
		{name: "unsafe pointer", value: nilUnsafePointer, want: true},
		{name: "channel value", value: make(chan struct{}), want: false},
		{name: "function value", value: func() {}, want: false},
		{name: "map value", value: map[string]string{}, want: false},
		{name: "pointer value", value: new(int), want: false},
		{name: "slice value", value: []string{}, want: false},
		{name: "unsafe pointer value", value: unsafe.Pointer(&unsafeValue), want: false},
		{name: "string scalar", value: "", want: false},
		{name: "integer scalar", value: 0, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsNil(test.value); got != test.want {
				t.Fatalf("IsNil(%T) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}
