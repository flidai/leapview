package composectl

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestQualificationLoginDiagnosticBufferConcurrentWritesPreserveRecords(t *testing.T) {
	const recordCount = 2000
	streams := []string{"stdout", "stderr"}
	var diagnostics qualificationLoginDiagnosticBuffer

	start := make(chan struct{})
	var snapshots sync.WaitGroup
	for range 2 {
		snapshots.Add(1)
		go func() {
			defer snapshots.Done()
			<-start
			for range recordCount {
				_ = diagnostics.Snapshot()
			}
		}()
	}

	var writers sync.WaitGroup
	for _, stream := range streams {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			for index := 0; index < recordCount; index++ {
				if _, err := diagnostics.Write([]byte(fmt.Sprintf("%s:%04d\n", stream, index))); err != nil {
					t.Errorf("write %s record %d: %v", stream, index, err)
				}
			}
		}()
	}
	close(start)
	writers.Wait()
	snapshots.Wait()

	contents := diagnostics.Snapshot()
	if len(contents) == 0 {
		t.Fatal("diagnostic snapshot is empty")
	}
	original := string(contents)
	contents[0] = 'X'
	if got := string(diagnostics.Snapshot()); got != original {
		t.Fatal("diagnostic snapshot aliases the buffer")
	}

	seen := make(map[string]int, len(streams))
	for _, line := range strings.Split(strings.TrimSuffix(original, "\n"), "\n") {
		stream, value, found := strings.Cut(line, ":")
		if !found {
			t.Fatalf("diagnostic record is not intact: %q", line)
		}
		index, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("diagnostic record %q has invalid index: %v", line, err)
		}
		if _, ok := seen[stream]; !ok {
			seen[stream] = 0
		}
		if index != seen[stream] {
			t.Fatalf("%s records out of order: got index %d, want %d", stream, index, seen[stream])
		}
		seen[stream]++
	}
	for _, stream := range streams {
		if seen[stream] != recordCount {
			t.Fatalf("%s records = %d, want %d", stream, seen[stream], recordCount)
		}
	}
}

func TestQualificationLoginFailureIncludesRedactedCombinedDiagnostics(t *testing.T) {
	bin := t.TempDir()
	leapview := filepath.Join(bin, "leapview")
	script := `#!/bin/sh
printf '%s\n' '{"schemaVersion":1,"type":"deviceChallenge","verificationUrl":"https://example.test/device","userCode":"ABCD-EFGH"}'
printf '%s\n' '{"schemaVersion":1,"type":"authenticated"}'
printf '%s\n' 'native credential storage unavailable token=secret-value' >&2
exit 1
`
	if err := os.WriteFile(leapview, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var challenge qualificationLoginChallenge
	err := runQualificationLogin(t.Context(), os.Environ(), QualificationClientWorkerOptions{
		Target: "https://example.test", SourceRoot: "dashboards", ProjectID: "project:example",
	}, func(value qualificationLoginChallenge) error {
		challenge = value
		return nil
	})
	if err == nil {
		t.Fatal("runQualificationLogin succeeded for a failing command")
	}
	if !strings.Contains(err.Error(), `"type":"deviceChallenge"`) ||
		!strings.Contains(err.Error(), `"type":"authenticated"`) {
		t.Fatalf("login error omitted stdout diagnostics: %v", err)
	}
	if !strings.Contains(err.Error(), "native credential storage unavailable") {
		t.Fatalf("login error omitted stderr diagnostics: %v", err)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("login error leaked a secret: %v", err)
	}
	if strings.Contains(err.Error(), "login event stream is incomplete") {
		t.Fatalf("login error did not preserve command failure precedence: %v", err)
	}
	if challenge.UserCode != "ABCD-EFGH" {
		t.Fatalf("challenge = %+v", challenge)
	}
}
