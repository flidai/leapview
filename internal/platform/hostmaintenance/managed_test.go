package hostmaintenance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedJournalBlocksOrdinaryHostMutations(t *testing.T) {
	for _, phase := range []string{"closing-work", "committed", "unknown", "succeeded", "recovered"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "managed-image-operation.json"), []byte(`{"version":1,"phase":"`+phase+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
			err := Check(root)
			terminal := phase == "succeeded" || phase == "recovered"
			if (err == nil) != terminal {
				t.Fatalf("phase=%s error=%v", phase, err)
			}
		})
	}
}
