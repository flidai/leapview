package ociref

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseImmutableAcceptsNamedSHA256Digest(t *testing.T) {
	value := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	parsed, err := ParseImmutable(value)
	require.NoError(t, err)
	require.Equal(t, value, parsed.Reference)
	require.Equal(t, "sha256-"+strings.Repeat("a", 64), parsed.Generation)
}

func TestParseImmutableRejectsMutableOrMalformedReferences(t *testing.T) {
	for _, value := range []string{
		"ghcr.io/flidai/leapview:latest",
		"ghcr.io/UPPER/leapview@sha256:" + strings.Repeat("a", 64),
		"ghcr.io/flidai/leapview@sha512:" + strings.Repeat("a", 128),
		" ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64),
	} {
		t.Run(value, func(t *testing.T) {
			_, err := ParseImmutable(value)
			require.Error(t, err)
		})
	}
}
