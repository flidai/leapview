package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

func newDeploymentIdempotencyKey(kind string, values ...string) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate %s operation identity: %w", kind, err)
	}
	keyValues := append([]string(nil), values...)
	keyValues = append(keyValues, hex.EncodeToString(nonce[:]))
	return deploymentIdempotencyKey(kind, keyValues...), nil
}

func deploymentIdempotencyKey(kind string, values ...string) string {
	digest := sha256.New()
	writeDeploymentHashValue(digest, kind)
	for _, value := range values {
		writeDeploymentHashValue(digest, value)
	}
	return "deployment-" + kind + "-" + hex.EncodeToString(digest.Sum(nil))
}

func writeDeploymentHashValue(digest io.Writer, value string) {
	fmt.Fprintf(digest, "%d:%s", len(value), value)
}

func canonicalManagedRevisionID(value string) bool {
	return platformdigest.ValidateSHA256Identity(value) == nil
}
