package secret

import (
	"crypto/sha256"
	"crypto/subtle"
)

func Equal(got, want string) bool {
	gotDigest := sha256.Sum256([]byte(got))
	wantDigest := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotDigest[:], wantDigest[:]) == 1
}

// EqualFixedBytes compares secrets with a fixed, non-secret length. Callers
// must not use it when the expected length itself must remain confidential.
func EqualFixedBytes(got, want []byte) bool {
	return subtle.ConstantTimeCompare(got, want) == 1
}
