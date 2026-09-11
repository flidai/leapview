package agent

import "testing"

func TestConversationRevisionIncludesTranscriptRevision(t *testing.T) {
	row := Conversation{
		ID: "conversation-1", PrincipalID: "principal-1", Title: "Chat",
		Status: ConversationStatusActive, TranscriptRevision: 1,
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	}
	first, err := ConversationRevision(row)
	if err != nil {
		t.Fatal(err)
	}
	row.TranscriptRevision++
	second, err := ConversationRevision(row)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("conversation ETag did not change with transcript revision")
	}
}
