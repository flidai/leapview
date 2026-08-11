package agent

import apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"

// ConversationRevision returns the transport-neutral strong revision for a
// conversation. Keep this derived from the fields exposed by the API so GET,
// PATCH, and every other command surface compare one canonical value.
func ConversationRevision(row Conversation) (string, error) {
	return apigencommand.RevisionToken(struct {
		ID         string `json:"id"`
		Principal  string `json:"principalId"`
		Title      string `json:"title"`
		Status     string `json:"status"`
		CreatedAt  string `json:"createdAt"`
		UpdatedAt  string `json:"updatedAt"`
		ArchivedAt string `json:"archivedAt,omitempty"`
	}{
		ID: row.ID, Principal: row.PrincipalID, Title: row.Title, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ArchivedAt: row.ArchivedAt,
	})
}
