package successor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrozenRejectionVectors(t *testing.T) {
	paths, err := filepath.Glob("testdata/rejected/*")
	if err != nil || len(paths) != 7 {
		t.Fatalf("expected seven rejection vectors: %v (%d)", err, len(paths))
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			raw = bytes.TrimSuffix(raw, []byte("\n"))
			set, e := goldenFixture(t, "minimal")
			if err := set.ValidateEvidence(e); err != nil {
				t.Fatalf("valid baseline failed: %v", err)
			}
			check := func() error {
				submitted := e
				candidate := set
				var err error
				switch {
				case strings.HasSuffix(path, ".set.json"):
					candidate, err = ParseRecoverySet3(raw)
				case strings.HasSuffix(path, ".manifest.json"):
					submitted.Manifest, err = ParseManagedManifest2(raw)
				case strings.HasSuffix(path, ".receipt.json"):
					submitted.Receipt, err = ParseReceipt(raw)
				default:
					t.Fatal("unclassified rejection vector")
				}
				if err != nil {
					return err
				}
				return candidate.ValidateEvidence(submitted)
			}
			first := check()
			if first == nil {
				t.Fatal("invalid evidence accepted")
			}
			for i := 0; i < 20; i++ {
				next := check()
				if next == nil || next.Error() != first.Error() {
					t.Fatalf("nondeterministic rejection: %v versus %v", first, next)
				}
			}
		})
	}
}
