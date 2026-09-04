package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBunSuccessfulAuditDoesNotRetry(t *testing.T) {
	contract := exceptionContract{}
	root, bin, log := scannerFixture(t)
	setFakeScannerEnv(t, bin, log, "bun-nonblocking")
	var stdout, stderr bytes.Buffer
	waits := 0
	r := &runner{
		root: root, timeout: time.Second, stdout: &stdout, stderr: &stderr,
		bunRetrySleep: func(time.Duration) { waits++ },
	}

	if err := r.scanBun(filepath.Join(root, "bun.lock"), &contract); err != nil {
		t.Fatalf("scanBun error = %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if got := countScannerInvocations(mustRead(t, log), "bun"); got != 1 || waits != 0 {
		t.Fatalf("successful audit invoked Bun %d times and waited %d times, want 1 and 0", got, waits)
	}
}

func TestBunTransportRetryIsProcessWideAcrossLockfiles(t *testing.T) {
	contract := exceptionContract{}
	root, bin, log := scannerFixture(t)
	setFakeScannerEnv(t, bin, log, "bun-transport-recovery-then-exhausted")
	var stdout, stderr bytes.Buffer
	var delays []time.Duration
	r := &runner{
		root: root, timeout: time.Second, stdout: &stdout, stderr: &stderr,
		bunRetrySleep: func(delay time.Duration) { delays = append(delays, delay) },
	}

	if err := r.scanBun(filepath.Join(root, "bun.lock"), &contract); err != nil {
		t.Fatalf("recovery scanBun error = %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "retrying once after transport failure") {
		t.Fatalf("retry diagnostic is missing: %s", stdout.String())
	}
	if err := r.scanBun(filepath.Join(root, "desktop/bun.lock"), &contract); err == nil || !strings.Contains(stderr.String(), "not valid JSON") {
		t.Fatalf("retry budget was not exhausted across lockfiles: err=%v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if got := countScannerInvocations(mustRead(t, log), "bun"); got != 3 {
		t.Fatalf("Bun invocations = %d, want one retry total across both lockfiles (3)\nlog=%s", got, mustRead(t, log))
	}
	if len(delays) != 1 || delays[0] != bunRetryBackoff {
		t.Fatalf("retry delays = %v, want one bounded backoff of %s", delays, bunRetryBackoff)
	}
}

func TestBunValidNoncriticalResultWithTransportFailureRetries(t *testing.T) {
	contract := exceptionContract{}
	root, bin, log := scannerFixture(t)
	setFakeScannerEnv(t, bin, log, "bun-valid-transport-recovery")
	var stdout, stderr bytes.Buffer
	r := &runner{
		root: root, timeout: time.Second, stdout: &stdout, stderr: &stderr,
		bunRetrySleep: func(time.Duration) {},
	}

	if err := r.scanBun(filepath.Join(root, "bun.lock"), &contract); err != nil {
		t.Fatalf("scanBun error = %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if got := countScannerInvocations(mustRead(t, log), "bun"); got != 2 {
		t.Fatalf("Bun invocations = %d, want transport-marked partial result retried once (2)", got)
	}
}

func TestBunTransportRetryExhaustionFailsClosedAndRedacts(t *testing.T) {
	contract := exceptionContract{}
	root, bin, log := scannerFixture(t)
	setFakeScannerEnv(t, bin, log, "bun-transport-exhausted")
	var stdout, stderr bytes.Buffer
	r := &runner{
		root: root, timeout: time.Second, stdout: &stdout, stderr: &stderr,
		bunRetrySleep: func(time.Duration) {},
	}

	if err := r.scanBun(filepath.Join(root, "bun.lock"), &contract); err == nil || !strings.Contains(stderr.String(), "not valid JSON") {
		t.Fatalf("exhausted Bun retry was not rejected: err=%v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if got := countScannerInvocations(mustRead(t, log), "bun"); got != 2 {
		t.Fatalf("Bun invocations = %d, want one retry total (2)\nlog=%s", got, mustRead(t, log))
	}
	if strings.Contains(stdout.String(), "sentinel_value") || !strings.Contains(stdout.String(), "[REDACTED]") {
		t.Fatalf("retry diagnostics were not redacted: %s", stdout.String())
	}
}

func TestBunUnknownMalformedOutputIsNotRetried(t *testing.T) {
	contract := exceptionContract{}
	root, bin, log := scannerFixture(t)
	setFakeScannerEnv(t, bin, log, "bun-unknown-malformed")
	var stdout, stderr bytes.Buffer
	r := &runner{root: root, timeout: time.Second, stdout: &stdout, stderr: &stderr}

	if err := r.scanBun(filepath.Join(root, "bun.lock"), &contract); err == nil || !strings.Contains(stderr.String(), "not valid JSON") {
		t.Fatalf("unknown malformed Bun output was not rejected: err=%v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if got := countScannerInvocations(mustRead(t, log), "bun"); got != 1 {
		t.Fatalf("Bun invocations = %d, want no retry (1)\nlog=%s", got, mustRead(t, log))
	}
}

func TestBunCriticalFindingWithTransportDiagnosticIsNotRetried(t *testing.T) {
	contract := exceptionContract{}
	root, bin, log := scannerFixture(t)
	setFakeScannerEnv(t, bin, log, "bun-critical-transport")
	var stdout, stderr bytes.Buffer
	r := &runner{root: root, timeout: time.Second, stdout: &stdout, stderr: &stderr}

	if err := r.scanBun(filepath.Join(root, "bun.lock"), &contract); err == nil || !strings.Contains(err.Error(), "status 1") {
		t.Fatalf("critical Bun finding was not rejected: err=%v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if got := countScannerInvocations(mustRead(t, log), "bun"); got != 1 {
		t.Fatalf("Bun invocations = %d, want no retry (1)\nlog=%s", got, mustRead(t, log))
	}
	if !strings.Contains(stdout.String(), "Timeout: audit request failed") || !strings.Contains(stdout.String(), "critical") {
		t.Fatalf("critical finding or transport diagnostic was masked: %s", stdout.String())
	}
	if strings.Contains(stderr.String(), "not valid JSON") {
		t.Fatalf("valid critical finding was misclassified as malformed: %s", stderr.String())
	}
}

func TestBunCommandDeadlineIsNotRetried(t *testing.T) {
	r := &runner{}
	for _, commandErr := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("command timed out: %w", context.DeadlineExceeded),
	} {
		result := commandResult{status: 1, err: commandErr, stderr: []byte("Timeout: audit request failed\n")}
		if r.shouldRetryBun(result) {
			t.Errorf("command lifecycle error %v was incorrectly retryable", commandErr)
		}
	}
}

func TestBunLifecycleErrorFailsBeforeAcceptingNoncriticalJSON(t *testing.T) {
	contract := exceptionContract{}
	for _, lifecycleErr := range []error{context.Canceled, context.DeadlineExceeded} {
		lifecycleErr := lifecycleErr
		t.Run(lifecycleErr.Error(), func(t *testing.T) {
			var calls, waits int
			var stdout, stderr bytes.Buffer
			r := &runner{
				stdout: &stdout, stderr: &stderr,
				bunRetrySleep: func(time.Duration) { waits++ },
				bunCommand: func(string, ...string) commandResult {
					calls++
					return commandResult{
						stdout: []byte(`{"pkg":[{"severity":"low"}]}`),
						status: 1,
						err:    fmt.Errorf("scanner lifecycle: %w", lifecycleErr),
					}
				},
			}

			err := r.scanBun("/fixture/bun.lock", &contract)
			if err == nil || !errors.Is(err, lifecycleErr) {
				t.Fatalf("scanBun error = %v, want wrapped %v", err, lifecycleErr)
			}
			if calls != 1 || waits != 0 {
				t.Fatalf("lifecycle result invoked Bun %d times and waited %d times, want 1 and 0", calls, waits)
			}
			if strings.Contains(stdout.String(), "no Critical findings") {
				t.Fatalf("lifecycle result was accepted as noncritical: %s", stdout.String())
			}
		})
	}
}

func TestBunCriticalFindingWithLifecycleErrorFails(t *testing.T) {
	contract := exceptionContract{}
	var stdout, stderr bytes.Buffer
	var calls int
	r := &runner{
		stdout: &stdout, stderr: &stderr,
		bunCommand: func(string, ...string) commandResult {
			calls++
			return commandResult{
				stdout: []byte(`{"pkg":[{"severity":"critical"}]}`),
				status: 1,
				err:    fmt.Errorf("scanner timeout: %w", context.DeadlineExceeded),
			}
		},
	}

	err := r.scanBun("/fixture/bun.lock", &contract)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("scanBun error = %v, want deadline failure", err)
	}
	if calls != 1 {
		t.Fatalf("lifecycle result invoked Bun %d times, want 1", calls)
	}
}

func TestBunTransportSignatureRequiresCompleteLine(t *testing.T) {
	r := &runner{}
	for _, diagnostic := range []string{
		"prefix Timeout: audit request failed",
		"Timeout: audit request failed: details",
		"ConnectionClosed: audit request failed while reading",
	} {
		if r.shouldRetryBun(commandResult{status: 70, stderr: []byte(diagnostic)}) {
			t.Errorf("substring diagnostic was incorrectly retryable: %q", diagnostic)
		}
	}
	if !r.shouldRetryBun(commandResult{status: 70, stderr: []byte(" scanner output\n Timeout: audit request failed \n")}) {
		t.Fatal("complete transport diagnostic line was not retryable")
	}
	if !r.shouldRetryBun(commandResult{status: 0, stderr: []byte("ConnectionClosed: audit request failed\n")}) {
		t.Fatal("contradictory successful status masked a transport failure")
	}
}

func countScannerInvocations(log, tool string) int {
	count := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, tool+"|") {
			count++
		}
	}
	return count
}
