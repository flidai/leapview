package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	managedTypeSpecFixture.cleanup()
	os.Exit(code)
}
