package access

import (
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
)

// RoleBindingRevision is the transport-neutral concurrency revision for a
// role binding. Every surface compares the same domain fields.
func RoleBindingRevision(row RoleBinding) (string, error) {
	return apigencommand.RevisionToken(struct {
		ID          string
		WorkspaceID string
		SubjectType SubjectType
		SubjectID   string
		Email       string
		DisplayName string
		GroupName   string
		Role        string
		CreatedAt   string
	}{
		ID: row.ID, WorkspaceID: row.WorkspaceID, SubjectType: row.SubjectType,
		SubjectID: row.SubjectID, Email: row.Email, DisplayName: row.DisplayName,
		GroupName: row.GroupName, Role: row.Role, CreatedAt: row.CreatedAt,
	})
}

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

// GroupRevision is the transport-neutral concurrency revision for a group.
func GroupRevision(row Group) (string, error) {
	return apigencommand.RevisionToken(struct {
		ID          string
		WorkspaceID string
		Provider    string
		ExternalID  string
		Name        string
		CreatedAt   string
	}{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Provider: row.Provider,
		ExternalID: row.ExternalID, Name: row.Name, CreatedAt: row.CreatedAt,
	})
}

// GrantRevision is the transport-neutral concurrency revision for a grant.
func GrantRevision(row Grant) (string, error) {
	return apigencommand.RevisionToken(struct {
		ID          string
		ObjectID    string
		ObjectType  SecurableType
		WorkspaceID string
		SubjectType SubjectType
		SubjectID   string
		Privilege   Privilege
		CreatedAt   string
	}{
		ID: row.ID, ObjectID: row.ObjectID, ObjectType: row.ObjectType,
		WorkspaceID: row.WorkspaceID, SubjectType: row.SubjectType,
		SubjectID: row.SubjectID, Privilege: row.Privilege, CreatedAt: row.CreatedAt,
	})
}

// DataPolicyRevision is the transport-neutral concurrency revision for a data
// policy. ExpressionJSON is intentionally included because policy semantics
// can change without any other field changing.
func DataPolicyRevision(row DataPolicy) (string, error) {
	return apigencommand.RevisionToken(struct {
		ID             string
		WorkspaceID    string
		ObjectID       string
		SubjectType    SubjectType
		SubjectID      string
		PolicyType     string
		ExpressionJSON string
		CreatedAt      string
		UpdatedAt      string
	}{
		ID: row.ID, WorkspaceID: row.WorkspaceID, ObjectID: row.ObjectID,
		SubjectType: row.SubjectType, SubjectID: row.SubjectID, PolicyType: row.PolicyType,
		ExpressionJSON: row.ExpressionJSON, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	})
}
