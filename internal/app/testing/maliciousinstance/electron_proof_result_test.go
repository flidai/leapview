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

func TestReadElectronProofResultPreservesStorageOperationDiagnostics(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		current    string
		diagnostic electronStorageDiagnostic
		want       string
	}{
		{
			name:    "renderer timeout",
			payload: `{"passed":false,"phase":"failed","error":"renderer script exceeded 5000ms","currentCheck":"storage.cross-profile.first.seed-renderer-state","storageDiagnostics":[{"operation":"first.seed-renderer-state","status":"failed","durationMs":5001}]}`,
			current: "storage.cross-profile.first.seed-renderer-state",
			diagnostic: electronStorageDiagnostic{
				Operation:  "first.seed-renderer-state",
				Status:     "failed",
				DurationMS: 5001,
			},
			want: "storageDiagnostics=[{first.seed-renderer-state failed 5001}]",
		},
		{
			name:    "unfinished operation at proof deadline",
			payload: `{"passed":false,"phase":"failed","error":"policy integration exceeded 40000ms","currentCheck":"storage.cross-profile.second.read-renderer-state","storageDiagnostics":[{"operation":"second.read-renderer-state","status":"running","durationMs":40000}]}`,
			current: "storage.cross-profile.second.read-renderer-state",
			diagnostic: electronStorageDiagnostic{
				Operation:  "second.read-renderer-state",
				Status:     "running",
				DurationMS: 40000,
			},
			want: "storageDiagnostics=[{second.read-renderer-state running 40000}]",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "electron-proof.json")
			if err := os.WriteFile(path, []byte(test.payload), 0o600); err != nil {
				t.Fatal(err)
			}
			processErr := errors.New("exit status 1")
			result, payload, err := readElectronProofResult(path, processErr, []byte("electron stderr"))
			if err != nil {
				t.Fatal(err)
			}
			if result.CurrentCheck != test.current {
				t.Fatalf("current check = %q, want %q", result.CurrentCheck, test.current)
			}
			if len(result.StorageDiagnostics) != 1 || result.StorageDiagnostics[0] != test.diagnostic {
				t.Fatalf("storage diagnostics = %v, want %v", result.StorageDiagnostics, test.diagnostic)
			}

			err = electronProofProcessFailure(result, payload, processErr, []byte("electron stderr"))
			if err == nil {
				t.Fatal("electronProofProcessFailure returned no error")
			}
			for _, want := range []string{`currentCheck="` + test.current + `"`, test.want} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}
