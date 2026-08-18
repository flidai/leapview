package module

import (
	"context"
	"strings"
	"testing"
)

func TestBuildRequiresSealedActiveStateResolver(t *testing.T) {
	_, err := Build(context.Background(), Config{RequireSealedCatalog: true})
	if err == nil || !strings.Contains(err.Error(), "authoritative active-state resolver") {
		t.Fatalf("Build() error = %v, want sealed resolver configuration error", err)
	}
}
