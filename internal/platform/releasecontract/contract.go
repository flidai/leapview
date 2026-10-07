// Package releasecontract describes application compatibility independently of
// the SQL revision. The embedded manifest is read from the exact source revision
// by release automation and checked again by the candidate-owned controller.
package releasecontract

import (
	_ "embed"
	"encoding/json"
)

const LegacyPermissions = "legacy-capabilities/v1"
const TypedPermissions = "leapview.permissions/v1"

//go:embed contract.json
var manifest []byte

type Contract struct {
	Version           int    `json:"version"`
	PermissionProfile string `json:"permissionProfile"`
	PublicationAPI    string `json:"publicationAPI"`
	HostTransition    string `json:"hostTransition"`
}

func Current() Contract {
	var c Contract
	if err := json.Unmarshal(manifest, &c); err != nil {
		panic(err)
	}
	return c
}

func KnownPermissionProfile(profile string) bool {
	return profile == LegacyPermissions || profile == TypedPermissions
}
