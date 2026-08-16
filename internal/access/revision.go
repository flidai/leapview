package access

import (
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
)

// PrincipalRevision is the transport-neutral concurrency revision for a
// principal. Keep this derived from domain fields so every command surface
// compares the same value without depending on an HTTP representation.
func PrincipalRevision(row Principal) (string, error) {
	return apigencommand.RevisionToken(struct {
		CreatedAt   string        `json:"createdAt"`
		DisplayName string        `json:"displayName"`
		Email       string        `json:"email"`
		ID          string        `json:"id"`
		Kind        PrincipalKind `json:"kind"`
		UpdatedAt   string        `json:"updatedAt"`
	}{
		ID: row.ID, Kind: row.Kind, Email: row.Email, DisplayName: row.DisplayName,
		CreatedAt: revisionTimestamp(row.CreatedAt), UpdatedAt: revisionTimestamp(row.UpdatedAt),
	})
}

func revisionTimestamp(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return ""
}

// GroupRevision is the transport-neutral concurrency revision for a global group.
func GroupRevision(row Group) (string, error) {
	return apigencommand.RevisionToken(struct {
		ID         string
		Provider   string
		ExternalID string
		Name       string
		CreatedAt  string
	}{
		ID: row.ID, Provider: row.Provider, ExternalID: row.ExternalID,
		Name: row.Name, CreatedAt: row.CreatedAt,
	})
}
