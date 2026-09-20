package postgres

import (
	"encoding/json"
	"fmt"
)

// canonicalizeBundleDocuments restores the canonical bytes accepted at
// admission after PostgreSQL JSONB has normalized their textual projection.
func canonicalizeBundleDocuments(bundle Bundle) (Bundle, error) {
	values := []*string{&bundle.AccessPolicyJSON, &bundle.DashboardPublicationsJSON, &bundle.DashboardAppearancesJSON}
	for index, label := range []string{"access policy", "dashboard publications", "dashboard appearances"} {
		canonical, err := canonicalServingDocument(*values[index])
		if err != nil {
			return Bundle{}, fmt.Errorf("decode serving %s: %w", label, err)
		}
		*values[index] = canonical
	}
	return bundle, nil
}

func canonicalServingDocument(raw string) (string, error) {
	normalized, err := canonicalObject(raw)
	if err != nil {
		return "", err
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(normalized), &value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(canonical) > 1<<20 {
		return "", fmt.Errorf("serving-state JSON exceeds 1 MiB")
	}
	return string(canonical), nil
}
