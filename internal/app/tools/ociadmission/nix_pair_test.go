package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNixPairRequiresBothExactPlatformReceipts(t *testing.T) {
	directory := t.TempDir()
	args := []string{"--directory", directory, "--kind", "site-image", "--run-id", "456", "--attempt", "1", "--producer-revision", strings.Repeat("b", 40)}
	var output bytes.Buffer
	for _, arch := range []string{"amd64", "arm64"} {
		opts, _, _ := nixProducedFixture(t, arch)
		name := filepath.Join(directory, "nix-admission-site-image-456-1-"+arch)
		if err := os.Rename(opts.output, name); err != nil {
			t.Fatal(err)
		}
		if arch == "amd64" {
			if err := runNixPair(args, &output, &output); err == nil {
				t.Fatal("single platform prematurely made pair successful")
			}
		}
	}
	if err := runNixPair(args, &output, &output); err != nil {
		t.Fatal(err)
	}
	if err := runNixPair(append(args, "--producer-revision", strings.Repeat("c", 40)), &output, &output); err == nil {
		t.Fatal("wrong verifier revision accepted")
	}
	if err := os.WriteFile(filepath.Join(directory, "nix-admission-site-image-456-1-arm64/evidence/go/site/govulncheck.json"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runNixPair(args, &output, &output); err == nil {
		t.Fatal("substituted ARM raw scan accepted")
	}
}
