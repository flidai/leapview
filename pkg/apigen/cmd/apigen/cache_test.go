package main

import (
	"os"
	"testing"
)

var managedTypeSpecCacheRoot string

func TestMain(m *testing.M) {
	cacheRoot, err := os.MkdirTemp("", "apigen-typespec-test-cache-")
	if err != nil {
		os.Stderr.WriteString("create managed typespec test cache: " + err.Error() + "\n")
		os.Exit(1)
	}
	managedTypeSpecCacheRoot = cacheRoot

	code := m.Run()
	if err := os.RemoveAll(cacheRoot); err != nil {
		os.Stderr.WriteString("remove managed typespec test cache: " + err.Error() + "\n")
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
