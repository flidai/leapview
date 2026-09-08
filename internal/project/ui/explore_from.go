package ui

import (
	"errors"
	"net/url"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
)

const canonicalDataExplorerURLVersion = "2"

// CanonicalDataExplorerHref builds the same v2 state URL emitted by the
// Data Explorer shell. The state is the canonical ExplorationSpec, rather
// than a dashboard query or a result-row projection, so the destination will
// rerun it against the recipient's current authorized serving state.
func CanonicalDataExplorerHref(spec exploration.ExplorationSpec) (string, error) {
	if strings.TrimSpace(spec.ModelID) == "" {
		return "", errors.New("exploration model id is required")
	}
	if err := exploration.ValidateShape(&spec); err != nil {
		return "", err
	}
	encoded, err := canonicalExplorationJSON(spec)
	if err != nil {
		return "", err
	}
	values := url.Values{"mode": {"explore"}, "v": {canonicalDataExplorerURLVersion}, "state": {string(encoded)}}
	return "/explore?" + values.Encode(), nil
}
