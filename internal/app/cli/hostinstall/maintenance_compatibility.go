package hostinstall

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// SourceCompatibility is collected from the exact admitted Git revisions, not
// the runner checkout. Qualification and OCI admission bind those revisions to
// the images. The host recomputes the decision instead of trusting a mode flag.
type SourceCompatibility struct {
	Schema     int               `json:"schema"`
	Migrations map[string]string `json:"migrations"`
	Engines    map[string]string `json:"engines"`
	RolePolicy string            `json:"rolePolicy"`
}

func classifySources(before, after SourceCompatibility) (string, []string, error) {
	for _, s := range []SourceCompatibility{before, after} {
		if s.Schema < 1 || len(s.Migrations) != s.Schema || len(s.Engines) == 0 || !digestPattern.MatchString("sha256:"+s.RolePolicy) {
			return "", nil, errors.New("incomplete immutable compatibility evidence")
		}
		versions := map[int]bool{}
		for name, hash := range s.Migrations {
			prefix, _, ok := strings.Cut(name, "_")
			n, err := strconv.Atoi(prefix)
			if !ok || err != nil || n < 1 || n > s.Schema || versions[n] || !digestPattern.MatchString("sha256:"+hash) {
				return "", nil, errors.New("invalid immutable migration history")
			}
			versions[n] = true
		}
	}
	if after.Schema < before.Schema {
		return "", nil, errors.New("downgrade requires recovery")
	}
	for name, hash := range before.Migrations {
		if after.Migrations[name] != hash {
			return "", nil, fmt.Errorf("historical migration changed: %s", name)
		}
	}
	var pending []string
	for name := range after.Migrations {
		if _, ok := before.Migrations[name]; !ok {
			pending = append(pending, name)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		a, _, _ := strings.Cut(pending[i], "_")
		b, _, _ := strings.Cut(pending[j], "_")
		x, _ := strconv.Atoi(a)
		y, _ := strconv.Atoi(b)
		return x < y
	})
	if !reflect.DeepEqual(before.Engines, after.Engines) {
		return "review-required", pending, nil
	}
	if len(pending) > 0 || before.RolePolicy != after.RolePolicy {
		return "database-upgrade-required", pending, nil
	}
	return "image-only", pending, nil
}
