//go:build linux

package hostinstall

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/flidai/leapview/internal/platform/buildinfo"
)

// Installed releases can expose either version flag. Select the advertised
// contract before invoking it; execution and identity errors never cause a
// fallback to another command or an unauthenticated source of build metadata.
func (e *NativeEffects) appVersion(ctx context.Context, name string) (buildinfo.Identity, error) {
	help, err := e.docker(ctx, "exec", name, "leapview", "version", "--help")
	if err != nil {
		return buildinfo.Identity{}, err
	}
	arguments, err := versionJSONArguments(help)
	if err != nil {
		return buildinfo.Identity{}, err
	}
	raw, err := e.docker(ctx, append([]string{"exec", name, "leapview", "version"}, arguments...)...)
	if err != nil {
		return buildinfo.Identity{}, err
	}
	var identity buildinfo.Identity
	var required struct {
		Dirty *bool `json:"dirty"`
	}
	if json.Unmarshal([]byte(raw), &identity) != nil || json.Unmarshal([]byte(raw), &required) != nil ||
		required.Dirty == nil || *required.Dirty || !sourceRevisionPattern.MatchString(identity.Revision) {
		return buildinfo.Identity{}, errors.New("runtime source identity is invalid")
	}
	return identity, nil
}

func versionJSONArguments(help string) ([]string, error) {
	legacy := false
	for _, line := range strings.Split(help, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "--format" && fields[1] == "string" {
			return []string{"--format", "json"}, nil
		}
		if len(fields) >= 1 && fields[0] == "--json" {
			legacy = true
		}
	}
	if legacy {
		return []string{"--json"}, nil
	}
	return nil, errors.New("runtime does not advertise a supported JSON version command")
}
