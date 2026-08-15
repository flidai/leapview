package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/flidai/leapview/internal/dashboard/authoring"
)

const authoringIdentifierEntropyBytes = 16

// newAuthoringIDGenerator allocates IDs from cryptographically random entropy.
// The reader is injectable so construction and entropy failures can be tested
// without weakening the production path or falling back to request IDs.
func newAuthoringIDGenerator(prefix string, read io.Reader) func() (string, error) {
	return func() (string, error) {
		if read == nil {
			return "", fmt.Errorf("generate %s id: entropy reader is required", prefix)
		}
		entropy := make([]byte, authoringIdentifierEntropyBytes)
		if _, err := io.ReadFull(read, entropy); err != nil {
			return "", fmt.Errorf("generate %s id: %w", prefix, err)
		}
		return prefix + "-" + hex.EncodeToString(entropy), nil
	}
}

type authoringIDGenerators struct {
	dashboard func() (authoring.DashboardID, error)
	draft     func() (authoring.DraftID, error)
	revision  func() (authoring.RevisionID, error)
}

func newAuthoringIDGenerators(read io.Reader) authoringIDGenerators {
	dashboard := newAuthoringIDGenerator("dashboard", read)
	draft := newAuthoringIDGenerator("draft", read)
	revision := newAuthoringIDGenerator("revision", read)
	return authoringIDGenerators{
		dashboard: func() (authoring.DashboardID, error) {
			value, err := dashboard()
			return authoring.DashboardID(value), err
		},
		draft: func() (authoring.DraftID, error) {
			value, err := draft()
			return authoring.DraftID(value), err
		},
		revision: func() (authoring.RevisionID, error) {
			value, err := revision()
			return authoring.RevisionID(value), err
		},
	}
}

func productionAuthoringIDGenerators() authoringIDGenerators {
	return newAuthoringIDGenerators(rand.Reader)
}
