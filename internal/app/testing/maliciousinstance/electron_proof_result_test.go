package maliciousinstance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadElectronProofResultClassifiesMissingEmptyAndMalformedDocuments(t *testing.T) {
	processErr := errors.New("Electron exited unexpectedly")
	output := []byte("electron stderr")

	tests := []struct {
		name       string
		contents   []byte
		write      bool
		want       []string
		wantAbsent []string
	}{
		{
			name:  "missing",
			want:  []string{"Electron proof result is missing", "Electron exited unexpectedly", "electron stderr"},
			write: false,
		},
		{
			name:     "empty",
			contents: []byte{},
			write:    true,
			want:     []string{"Electron proof result is empty", "size=0 bytes", `raw result: ""`, "Electron exited unexpectedly", "electron stderr"},
		},
		{
			name:       "malformed",
			contents:   []byte(`{"passed":`),
			write:      true,
			want:       []string{"Electron proof result is malformed", "size=10 bytes", `raw result: "{\"passed\":"`, "unexpected end of JSON input", "Electron exited unexpectedly", "electron stderr"},
			wantAbsent: []string{"Electron proof result is empty"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "electron-proof.json")
			if test.write {
				if err := os.WriteFile(path, test.contents, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			_, _, err := readElectronProofResult(path, processErr, output)
			if err == nil {
				t.Fatal("readElectronProofResult returned no error")
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			for _, unwanted := range test.wantAbsent {
				if strings.Contains(err.Error(), unwanted) {
					t.Errorf("error %q unexpectedly contains %q", err, unwanted)
				}
			}
		})
	}
}

func TestElectronProofProcessFailurePreservesValidResultAndProcessDiagnostics(t *testing.T) {
	payload := []byte(`{"passed":false,"phase":"failed","error":"permission proof failed"}`)
	result := electronProofResult{
		Passed: false,
		Error:  "permission proof failed",
	}
	processErr := errors.New("exit status 1")

	err := electronProofProcessFailure(result, payload, processErr, []byte("electron stderr"))
	if err == nil {
		t.Fatal("electronProofProcessFailure returned no error")
	}
	for _, want := range []string{
		"valid failure result",
		"permission proof failed",
		"exit status 1",
		"electron stderr",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}
