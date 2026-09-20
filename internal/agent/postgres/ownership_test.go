package postgres

import (
	"testing"

	"github.com/flidai/leapview/internal/agent"
)

const (
	agentOwnershipOwner  = "ownership-owner"
	agentOwnershipTarget = "ownership-target"
)

func TestPostgreSQL18OwnershipTransferIsIdempotentAndRetainsMessages(t *testing.T) {
	db, repo := agentPostgresTestRepo(t, "ownership_transfer")
	ctx := t.Context()
	conversation, err := repo.CreateConversation(ctx, agent.ConversationInput{PrincipalID: agentOwnershipOwner, Title: "Owned conversation", MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AppendMessage(ctx, agent.MessageInput{PrincipalID: agentOwnershipOwner, ConversationID: conversation.ID, Role: agent.MessageRoleUser, ContentText: "retained evidence", ContentJSON: `{}`}); err != nil {
		t.Fatal(err)
	}

	first, err := repo.TransferOwnedObjects(ctx, agentOwnershipOwner, agentOwnershipTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Objects) != 1 || first.Objects[0].Kind != "agent_conversation" {
		t.Fatalf("first transfer report = %#v", first)
	}
	second, err := repo.TransferOwnedObjects(ctx, agentOwnershipOwner, agentOwnershipTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Objects) != 0 {
		t.Fatalf("retry transfer report = %#v, want no-op", second)
	}

	var principal, status string
	if err := db.QueryRow(ctx, `SELECT principal_id,status FROM agent.conversations WHERE id=$1`, conversation.ID).Scan(&principal, &status); err != nil {
		t.Fatal(err)
	}
	if principal != agentOwnershipTarget || status != agent.ConversationStatusActive {
		t.Fatalf("transferred conversation = principal %q status %q", principal, status)
	}
	var messages int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM agent.messages WHERE conversation_id=$1`, conversation.ID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 1 {
		t.Fatalf("retained messages = %d, want 1", messages)
	}
	if report, err := repo.ListOwnedObjects(ctx, agentOwnershipOwner); err != nil {
		t.Fatal(err)
	} else if len(report.Objects) != 0 {
		t.Fatalf("source ownership after transfer = %#v", report)
	}
	if report, err := repo.ListOwnedObjects(ctx, agentOwnershipTarget); err != nil {
		t.Fatal(err)
	} else if len(report.Objects) != 1 {
		t.Fatalf("target ownership after transfer = %#v", report)
	}
}

func TestPostgreSQL18OwnershipTombstoneIsIdempotentAndRetainsMessages(t *testing.T) {
	db, repo := agentPostgresTestRepo(t, "ownership_tombstone")
	ctx := t.Context()
	conversation, err := repo.CreateConversation(ctx, agent.ConversationInput{PrincipalID: agentOwnershipOwner, Title: "Owned conversation", MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AppendMessage(ctx, agent.MessageInput{PrincipalID: agentOwnershipOwner, ConversationID: conversation.ID, Role: agent.MessageRoleUser, ContentText: "retained evidence", ContentJSON: `{}`}); err != nil {
		t.Fatal(err)
	}

	first, err := repo.TombstoneOwnedObjects(ctx, agentOwnershipOwner)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Objects) != 1 || first.Objects[0].Lifecycle != "deleted" {
		t.Fatalf("first tombstone report = %#v", first)
	}
	second, err := repo.TombstoneOwnedObjects(ctx, agentOwnershipOwner)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Objects) != 0 {
		t.Fatalf("retry tombstone report = %#v, want no-op", second)
	}

	var status string
	if err := db.QueryRow(ctx, `SELECT status FROM agent.conversations WHERE id=$1`, conversation.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != agent.ConversationStatusArchived {
		t.Fatalf("tombstoned conversation status = %q, want archived", status)
	}
	var messages int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM agent.messages WHERE conversation_id=$1`, conversation.ID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 1 {
		t.Fatalf("retained messages = %d, want 1", messages)
	}
	if report, err := repo.ListOwnedObjects(ctx, agentOwnershipOwner); err != nil {
		t.Fatal(err)
	} else if len(report.Objects) != 0 {
		t.Fatalf("ownership after tombstone = %#v", report)
	}
}
