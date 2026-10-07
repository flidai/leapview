package releasecontract

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// SourceCompatibility is collected from the exact admitted Git revisions, not
// the runner checkout. Qualification and OCI admission bind those revisions to
// the images. The host recomputes the decision instead of trusting a mode flag.
type SourceCompatibility struct {
	PermissionProfile string            `json:"permissionProfile"`
	Schema            int               `json:"schema"`
	Migrations        map[string]string `json:"migrations"`
	Engines           map[string]string `json:"engines"`
	RolePolicy        string            `json:"rolePolicy"`
}

var sourceDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ClassifySources recomputes preflight compatibility from immutable source
// evidence. It does not authorize migration execution, startup, or rollback;
// adapters must separately bind the evidence to admitted artifacts and enforce
// their ownership, maintenance, and recovery contracts.
func ClassifySources(before, after SourceCompatibility) (string, []string, error) {
	for _, s := range []SourceCompatibility{before, after} {
		if !KnownPermissionProfile(s.PermissionProfile) || s.Schema < 1 || len(s.Migrations) != s.Schema || len(s.Engines) == 0 || !sourceDigestPattern.MatchString(s.RolePolicy) {
			return "", nil, errors.New("incomplete immutable compatibility evidence")
		}
		if err := validateEngineIdentities(s.Engines); err != nil {
			return "", nil, err
		}
		versions := map[int]bool{}
		for name, hash := range s.Migrations {
			prefix, _, ok := strings.Cut(name, "_")
			n, err := strconv.Atoi(prefix)
			if !ok || err != nil || n < 1 || n > s.Schema || versions[n] || !sourceDigestPattern.MatchString(hash) {
				return "", nil, errors.New("invalid immutable migration history")
			}
			versions[n] = true
		}
	}
	if after.PermissionProfile != Current().PermissionProfile {
		return "", nil, errors.New("candidate permission contract differs from controller")
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
	if len(pending) > 0 || before.RolePolicy != after.RolePolicy || before.PermissionProfile != after.PermissionProfile {
		return "database-upgrade-required", pending, nil
	}
	return "image-only", pending, nil
}

func validateEngineIdentities(engines map[string]string) error {
	// These are the same durable engine families collected from the admitted
	// revision's go.mod by the release planner. Equality of two partial maps is
	// not evidence that either source is compatible.
	found := map[string]bool{"github.com/duckdb/": false, "github.com/riverqueue/": false}
	for name, version := range engines {
		if !semver.IsValid(version) {
			return fmt.Errorf("invalid immutable engine version: %q", name)
		}
		known := false
		for prefix := range found {
			if strings.HasPrefix(name, prefix) && len(name) > len(prefix) && !strings.ContainsAny(name, " \t\r\n") {
				found[prefix], known = true, true
			}
		}
		if !known {
			return fmt.Errorf("unknown immutable engine identity: %q", name)
		}
	}
	for prefix, present := range found {
		if !present {
			return fmt.Errorf("missing immutable engine family: %s", prefix)
		}
	}
	return nil
}
