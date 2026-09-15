package postgres

import "fmt"

// canonicalizeBundleDocuments restores the canonical bytes accepted at
// admission after PostgreSQL JSONB has normalized their textual projection.
func canonicalizeBundleDocuments(bundle Bundle) (Bundle, error) {
	values := []*string{&bundle.AccessPolicyJSON, &bundle.DashboardPublicationsJSON, &bundle.DashboardAppearancesJSON}
	for index, label := range []string{"access policy", "dashboard publications", "dashboard appearances"} {
		canonical, err := canonicalObject(*values[index])
		if err != nil {
			return Bundle{}, fmt.Errorf("decode serving %s: %w", label, err)
		}
		*values[index] = canonical
	}
	return bundle, nil
}
