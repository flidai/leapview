package secret

import "testing"

func TestEqualComparesBearerSecrets(t *testing.T) {
	want := "0123456789abcdef0123456789abcdef"
	for _, tt := range []struct {
		name string
		got  string
		want bool
	}{
		{name: "match", got: want, want: true},
		{name: "same length mismatch", got: "0123456789abcdef0123456789abcdee", want: false},
		{name: "short mismatch", got: "short", want: false},
		{name: "long mismatch", got: want + "extra", want: false},
		{name: "empty mismatch", got: "", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Equal(tt.got, want); got != tt.want {
				t.Fatalf("Equal(...) = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEqualFixedBytes(t *testing.T) {
	want := []byte("fixed-length-proxy-credential")
	for _, tt := range []struct {
		name string
		got  []byte
		want bool
	}{
		{name: "match", got: []byte("fixed-length-proxy-credential"), want: true},
		{name: "same length mismatch", got: []byte("fixed-length-proxy-credentiam")},
		{name: "short mismatch", got: []byte("short")},
		{name: "long mismatch", got: []byte("fixed-length-proxy-credential-extra")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := EqualFixedBytes(tt.got, want); got != tt.want {
				t.Fatalf("EqualFixedBytes(...) = %v, want %v", got, tt.want)
			}
		})
	}
}
