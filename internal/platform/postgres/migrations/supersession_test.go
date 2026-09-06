package migrations

import (
	"context"
	"strings"
	"testing"
)

func TestHistoricalIdentityLedgerBytesFrozen(t *testing.T) {
	if sqlChecksum(supersessionJSON) != "dfd81b0dacf1dbcdf06f02127ca88ec563adeec9de2b452d40037cbdaa1bc5dd" {
		t.Fatal("immutable supersession declaration changed")
	}
	if got := IdentityLedgerChecksum(); got != "42f0dc3dacbbf4fa06aef8e2fcbcb6d3559fd97e2907a18fc596c1c4c01152f1" {
		t.Fatalf("immutable migration 002 changed: %s", got)
	}
	if IdentityLedgerReplacementChecksum() != "61385b011fa9d235f97dacfc783682a01d869bc06f043013b9ff0988dcd147e4" {
		t.Fatal("immutable replacement changed")
	}
	if identityLedgerReplacementSQL != strings.ReplaceAll(identityLedgerSQL, "ON TABLES\n", "ON TABLE\n") {
		t.Fatal("replacement differs beyond the two approved syntax corrections")
	}
}

func TestSupersessionPlanning(t *testing.T) {
	for _, original := range []bool{false, true} {
		tx := &recordingTx{revisions: validRevisions()}
		if !original {
			tx.revisions[2] = recordingRow{revision: 2, migrationID: IdentityLedgerReplacementMigrationID, checksum: IdentityLedgerReplacementChecksum()}
		}
		if err := Verify(context.Background(), tx); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutation := range []string{"checksum", "id", "gap", "future"} {
		t.Run(mutation, func(t *testing.T) {
			tx := &recordingTx{revisions: validRevisions()}
			row := tx.revisions[2]
			switch mutation {
			case "checksum":
				row.checksum = strings.Repeat("0", 64)
				tx.revisions[2] = row
			case "id":
				row.migrationID = "unknown"
				tx.revisions[2] = row
			case "gap":
				delete(tx.revisions, 2)
			case "future":
				tx.revisions[99] = recordingRow{revision: 99, migrationID: "future", checksum: strings.Repeat("0", 64)}
			}
			if err := Apply(context.Background(), tx); err == nil {
				t.Fatal("invalid history accepted")
			}
			if len(tx.sqls) != 1 || tx.sqls[0] != migrationLockSQL {
				t.Fatal("SQL executed before validation")
			}
			if err := Verify(context.Background(), tx); err == nil {
				t.Fatal("Verify accepted invalid history")
			}
		})
	}
	for prefix := 0; prefix < len(ordered()); prefix++ {
		tx := &recordingTx{revisions: validRevisions()}
		for revision := range tx.revisions {
			if revision > int64(prefix) {
				delete(tx.revisions, revision)
			}
		}
		if err := Apply(context.Background(), tx); err != nil {
			t.Fatalf("prefix %d: %v", prefix, err)
		}
		if len(tx.sqls) != 1+2*(len(ordered())-prefix) {
			t.Fatalf("prefix %d executed unexpected SQL", prefix)
		}
		if prefix < 2 && tx.revisions[2].migrationID != IdentityLedgerReplacementMigrationID {
			t.Fatal("recorded wrong execution identity")
		}
	}
}

func TestSupersessionMetadataRequired(t *testing.T) {
	original := supersessionJSON
	t.Cleanup(func() { supersessionJSON = original })
	for _, invalid := range []string{"", "{}", strings.Replace(original, IdentityLedgerReplacementMigrationID, "unknown", 1)} {
		supersessionJSON = invalid
		tx := &recordingTx{revisions: validRevisions()}
		if Apply(context.Background(), tx) == nil || Verify(context.Background(), tx) == nil {
			t.Fatal("invalid lineage metadata accepted")
		}
		if len(tx.sqls) != 0 {
			t.Fatal("SQL executed with invalid metadata")
		}
	}
}

func TestAppliedHistoryNeverReexecutesSQL(t *testing.T) {
	tx := &recordingTx{revisions: validRevisions()}
	if err := Apply(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	for _, sql := range tx.sqls {
		if sql == IdentityLedgerSQL() || sql == BaselineSQL() {
			t.Fatal("already applied migration executed before history validation")
		}
	}
}
