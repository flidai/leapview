package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestAuthoringIDGeneratorsProduceValidDistinctPrefixedIDs(t *testing.T) {
	entropyBytes := append(bytes.Repeat([]byte{0x01}, authoringIdentifierEntropyBytes), bytes.Repeat([]byte{0x02}, authoringIdentifierEntropyBytes)...)
	entropy := bytes.NewReader(entropyBytes)
	generators := newAuthoringIDGenerators(entropy)

	first, err := generators.dashboard()
	if err != nil {
		t.Fatal(err)
	}
	second, err := generators.dashboard()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("dashboard IDs collided: %q", first)
	}
	if !strings.HasPrefix(first.String(), "dashboard-") || !strings.HasPrefix(second.String(), "dashboard-") {
		t.Fatalf("dashboard IDs lack semantic prefix: %q %q", first, second)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("first dashboard ID is invalid: %v", err)
	}
	if err := second.Validate(); err != nil {
		t.Fatalf("second dashboard ID is invalid: %v", err)
	}

	draft, err := newAuthoringIDGenerators(bytes.NewReader(bytes.Repeat([]byte{0x02}, authoringIdentifierEntropyBytes))).draft()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(draft.String(), "draft-") {
		t.Fatalf("draft ID lacks semantic prefix: %q", draft)
	}
	if err := draft.Validate(); err != nil {
		t.Fatalf("draft ID is invalid: %v", err)
	}

	revision, err := newAuthoringIDGenerators(bytes.NewReader(bytes.Repeat([]byte{0x03}, authoringIdentifierEntropyBytes))).revision()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(revision.String(), "revision-") {
		t.Fatalf("revision ID lacks semantic prefix: %q", revision)
	}
	if err := revision.Validate(); err != nil {
		t.Fatalf("revision ID is invalid: %v", err)
	}
}

func TestAuthoringIDGeneratorReportsEntropyErrors(t *testing.T) {
	want := errors.New("entropy unavailable")
	generator := newAuthoringIDGenerator("dashboard", errorReader{err: want})
	if _, err := generator(); !errors.Is(err, want) {
		t.Fatalf("generator error = %v, want wrapped %v", err, want)
	}
	if _, err := newAuthoringIDGenerator("dashboard", nil)(); err == nil {
		t.Fatal("nil entropy reader unexpectedly succeeded")
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
