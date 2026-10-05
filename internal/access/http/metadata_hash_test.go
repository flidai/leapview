package http

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

type metadataIdentityRepository struct {
	access.Repository
	management access.PrincipalIdentityManagement
}

func (r metadataIdentityRepository) PrincipalIdentityManagement(context.Context, string) (access.PrincipalIdentityManagement, error) {
	return r.management, nil
}

func TestPrincipalMetadataHashesCapabilitiesWithoutCredentials(t *testing.T) {
	principal := access.Principal{ID: "principal_1", Kind: access.PrincipalKindUser, Email: "user@example.test", DisplayName: "User", CreatedAt: "2026-10-04T00:00:00Z"}
	handler := Handler{LocalPasswordEnabled: true}
	management := access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal, HasLocalPassword: true}
	for _, current := range []bool{true, false} {
		project := func(management access.PrincipalIdentityManagement) map[string]any {
			t.Helper()
			var dto map[string]any
			var err error
			if current {
				dto, err = handler.currentPrincipalResponseFor(context.Background(), principal, management, nil, true)
			} else {
				dto, err = handler.principalAdministrationDTO(context.Background(), metadataIdentityRepository{management: management}, principal, "admin")
			}
			if err != nil {
				t.Fatal(err)
			}
			return dto
		}
		dto := project(management)
		var keys []string
		for key := range dto {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		expected := []string{"blockedAt", "capabilities", "createdAt", "disabledAt", "displayName", "email", "id", "identityManagement", "kind", "updatedAt"}
		// An explicit response allowlist prevents credential/verifier fields from
		// accidentally entering the representation (and hence its metadata hash).
		if !reflect.DeepEqual(keys, expected) {
			t.Fatalf("principal response fields=%v", keys)
		}
		encoded, err := json.Marshal(dto)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err = json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		if resourceETag(dto) != resourceETag(project(management)) || resourceETag(dto) != resourceETag(wire) {
			t.Fatal("unchanged representation changed ETag")
		}
		changed := management
		changed.HasLocalPassword = false
		changedDTO := project(changed)
		if resourceETag(dto) == resourceETag(changedDTO) {
			t.Fatal("capability change did not update ETag")
		}
		if apiItemPageKey(dto) != principal.CreatedAt+"\x00"+principal.ID || apiItemPageKey(dto) != apiItemPageKey(changedDTO) {
			t.Fatal("principal pagination must retain identity across capability changes")
		}
		// The generic fallback hashes ordinary metadata when no resource ID exists.
		metadata := dto["capabilities"]
		if apiItemPageKey(metadata) != apiItemPageKey(metadata) || apiItemPageKey(metadata) == apiItemPageKey(changedDTO["capabilities"]) {
			t.Fatal("metadata page key is not deterministic/content-sensitive")
		}
	}
}
