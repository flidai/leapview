//go:build linux || darwin

package developmentprofile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestLoadRejectsFIFOWithoutOpeningIt(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "profile.yaml")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadProfile(LoadOptions{CheckoutRoot: root, ProfileFile: path})
	assertDiagnostic(t, err, "profile.file_type", "")
}

func TestReadProfileContentRejectsSameFileMutation(t *testing.T) {
	root := t.TempDir()
	path := writeProfile(t, root, validEmptyProfile)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	reader := &mutatingProfileReader{
		File:        file,
		path:        path,
		replacement: []byte("version: 1\nprofiles:\n  other:\n    connections: {}\n"),
	}
	if len(reader.replacement) != len(validEmptyProfile) {
		t.Fatal("same-size mutation fixture changed size")
	}
	_, err = readProfileContent(reader, path, info)
	assertDiagnostic(t, err, "profile.file_changed", "")
}

type mutatingProfileReader struct {
	*os.File
	path        string
	replacement []byte
	mutated     bool
}

func (reader *mutatingProfileReader) Read(buffer []byte) (int, error) {
	if !reader.mutated {
		reader.mutated = true
		if err := os.WriteFile(reader.path, reader.replacement, 0o600); err != nil {
			return 0, err
		}
		if err := os.Chtimes(reader.path, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
			return 0, err
		}
	}
	return reader.File.Read(buffer)
}
