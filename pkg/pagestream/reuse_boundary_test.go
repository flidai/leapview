package pagestream

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageHasNoApplicationSpecificDependencies(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{
			"window.LeapView",
			"github.com/flidai/leapview",
			"lv_client_id",
		} {
			if strings.Contains(string(body), forbidden) {
				t.Fatalf("%s contains application-specific identifier %q", file, forbidden)
			}
		}
	}
}
