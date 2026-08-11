package ociref

import (
	"fmt"
	"strings"

	"github.com/distribution/reference"
	ocidigest "github.com/opencontainers/go-digest"
)

// Immutable is a validated named OCI image reference pinned by a SHA-256
// digest. Generation is safe to use as one filesystem path component.
type Immutable struct {
	Reference  string
	Generation string
}

func ParseImmutable(value string) (Immutable, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return Immutable{}, fmt.Errorf("image must be a canonical repository@sha256 digest")
	}
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return Immutable{}, fmt.Errorf("parse image reference: %w", err)
	}
	digested, ok := named.(reference.Digested)
	if !ok {
		return Immutable{}, fmt.Errorf("image must be pinned by digest")
	}
	digest := digested.Digest()
	if digest.Algorithm() != ocidigest.SHA256 {
		return Immutable{}, fmt.Errorf("image digest must use sha256")
	}
	if err := digest.Validate(); err != nil {
		return Immutable{}, fmt.Errorf("validate image digest: %w", err)
	}
	return Immutable{
		Reference:  value,
		Generation: strings.ReplaceAll(digest.String(), ":", "-"),
	}, nil
}

func ValidateImmutable(value string) error {
	_, err := ParseImmutable(value)
	return err
}
