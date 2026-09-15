package manageddata

import (
	"strconv"
	"strings"
	"testing"
)

func TestManifestCanonicalizesFilesAndProducesStableRevisionID(t *testing.T) {
	first := Manifest{Files: []File{
		{Path: "orders/2025.parquet", Size: 22, SHA256: strings.Repeat("b", 64)},
		{Path: "customers.csv", Size: 11, SHA256: strings.Repeat("a", 64)},
	}}
	second := Manifest{Files: []File{first.Files[1], first.Files[0]}}

	firstBytes, err := first.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := second.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatalf("canonical manifests differ:\n%s\n%s", firstBytes, secondBytes)
	}
	if first.RevisionID() != second.RevisionID() {
		t.Fatalf("revision IDs differ: %q != %q", first.RevisionID(), second.RevisionID())
	}
	if got := first.RevisionID(); !strings.HasPrefix(got, "sha256:") || len(got) != len("sha256:")+64 {
		t.Fatalf("RevisionID() = %q", got)
	}
}

func TestValidateRevisionIDRequiresCanonicalSHA256Identity(t *testing.T) {
	valid := "sha256:" + strings.Repeat("a", 64)
	if err := ValidateRevisionID(valid); err != nil {
		t.Fatalf("ValidateRevisionID(%q) error = %v", valid, err)
	}
	for _, invalid := range []string{
		strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("A", 64),
		"sha256:" + strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("z", 64),
		"sha512:" + strings.Repeat("a", 64),
	} {
		if err := ValidateRevisionID(invalid); err == nil {
			t.Fatalf("ValidateRevisionID(%q) error = nil", invalid)
		}
	}
}

func TestManifestRejectsUnsafeOrAmbiguousPaths(t *testing.T) {
	tests := []struct {
		name  string
		files []File
		want  string
	}{
		{name: "absolute", files: []File{{Path: "/orders.csv", Size: 1, SHA256: strings.Repeat("a", 64)}}, want: "relative"},
		{name: "traversal", files: []File{{Path: "../orders.csv", Size: 1, SHA256: strings.Repeat("a", 64)}}, want: "traversal"},
		{name: "backslash", files: []File{{Path: `folder\orders.csv`, Size: 1, SHA256: strings.Repeat("a", 64)}}, want: "forward slashes"},
		{name: "duplicate", files: []File{
			{Path: "orders.csv", Size: 1, SHA256: strings.Repeat("a", 64)},
			{Path: "orders.csv", Size: 1, SHA256: strings.Repeat("b", 64)},
		}, want: "duplicate"},
		{name: "case collision", files: []File{
			{Path: "Orders.csv", Size: 1, SHA256: strings.Repeat("a", 64)},
			{Path: "orders.csv", Size: 1, SHA256: strings.Repeat("b", 64)},
		}, want: "case-folding"},
		{name: "bad digest", files: []File{{Path: "orders.csv", Size: 1, SHA256: "not-a-digest"}}, want: "SHA-256"},
		{name: "negative size", files: []File{{Path: "orders.csv", Size: -1, SHA256: strings.Repeat("a", 64)}}, want: "size"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (Manifest{Files: test.files}).Validate(Limits{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestManifestEnforcesLimits(t *testing.T) {
	manifest := Manifest{Files: []File{
		{Path: "a.csv", Size: 7, SHA256: strings.Repeat("a", 64)},
		{Path: "b.csv", Size: 8, SHA256: strings.Repeat("b", 64)},
	}}

	tests := []struct {
		name   string
		limits Limits
		want   string
	}{
		{name: "file count", limits: Limits{MaxFiles: 1}, want: "file count"},
		{name: "file size", limits: Limits{MaxFileBytes: 6}, want: "maximum file size"},
		{name: "revision size", limits: Limits{MaxRevisionBytes: 14}, want: "revision size"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := manifest.Validate(test.limits)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestManifestAdmissionBoundaryIncludesFormerCaptureLimit(t *testing.T) {
	for _, count := range []int{4096, 4097, MaxManifestFiles, MaxManifestFiles + 1} {
		files := make([]File, count)
		for index := range files {
			files[index] = File{
				Path:   "file-" + strings.Repeat("x", 4) + "/" + strings.Repeat("n", 6) + strconv.Itoa(index),
				Size:   0,
				SHA256: strings.Repeat("a", 64),
			}
		}
		// Keep this test focused on the hard file-count contract. The
		// configured service limit may be lower, but the shared upper bound
		// must admit the former 4,096/4,097 capture boundary.
		err := (Manifest{Files: files}).Validate(Limits{MaxFiles: MaxManifestFiles})
		if count <= MaxManifestFiles && err != nil {
			t.Fatalf("file count %d rejected at shared admission bound: %v", count, err)
		}
		if count > MaxManifestFiles && err == nil {
			t.Fatalf("file count %d accepted above shared admission bound", count)
		}
	}
}

func TestManifestDiffUsesContentIdentity(t *testing.T) {
	oldManifest := Manifest{Files: []File{
		{Path: "keep.csv", Size: 1, SHA256: strings.Repeat("a", 64)},
		{Path: "change.csv", Size: 1, SHA256: strings.Repeat("b", 64)},
		{Path: "remove.csv", Size: 1, SHA256: strings.Repeat("c", 64)},
	}}
	newManifest := Manifest{Files: []File{
		{Path: "keep.csv", Size: 1, SHA256: strings.Repeat("a", 64)},
		{Path: "change.csv", Size: 2, SHA256: strings.Repeat("d", 64)},
		{Path: "add.csv", Size: 1, SHA256: strings.Repeat("e", 64)},
	}}

	diff := DiffManifests(oldManifest, newManifest)
	if len(diff.Unchanged) != 1 || diff.Unchanged[0].Path != "keep.csv" {
		t.Fatalf("unchanged = %#v", diff.Unchanged)
	}
	if len(diff.Changed) != 1 || diff.Changed[0].Path != "change.csv" {
		t.Fatalf("changed = %#v", diff.Changed)
	}
	if len(diff.Added) != 1 || diff.Added[0].Path != "add.csv" {
		t.Fatalf("added = %#v", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0].Path != "remove.csv" {
		t.Fatalf("removed = %#v", diff.Removed)
	}
}
