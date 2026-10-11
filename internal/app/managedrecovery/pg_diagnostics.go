package managedrecovery

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// Keep provider text private and bounded. Only fixed failure categories and
// numeric process exit codes may cross the recovery error boundary.
type postgresFailureOutput struct{ tail []byte }

func (output *postgresFailureOutput) Write(value []byte) (int, error) {
	const limit = 16384
	n := len(value)
	if n >= limit {
		output.tail = append(output.tail[:0], value[n-limit:]...)
	} else {
		if excess := len(output.tail) + n - limit; excess > 0 {
			copy(output.tail, output.tail[excess:])
			output.tail = output.tail[:len(output.tail)-excess]
		}
		output.tail = append(output.tail, value...)
	}
	return n, nil
}

func postgresProcessFailure(message string, processErr error, output *postgresFailureOutput) error {
	category := "unclassified"
	for _, entry := range [][2]string{
		{"Permission denied", "permission-denied"},
		{"Operation not permitted", "operation-not-permitted"},
		{"Read-only file system", "read-only-filesystem"},
		{"No such file or directory", "missing-file"},
		{"Address already in use", "address-in-use"},
		{"Cannot allocate memory", "memory-unavailable"},
		{"No space left on device", "storage-full"},
		{"recovery ended before configured recovery target was reached", "recovery-target-unreached"},
		{"has invalid permissions", "invalid-directory-permissions"},
		{"has wrong ownership", "invalid-directory-owner"},
		{"Unix-domain socket path", "unix-socket-path"},
		{"could not look up effective user ID", "user-lookup"},
		{"invalid value for parameter", "invalid-configuration"},
		{"could not load private key file", "tls-key"},
		{"shared memory", "shared-memory"},
		{"bwrap:", "kernel-confinement"},
	} {
		if strings.Contains(string(output.tail), entry[0]) {
			category = entry[1]
			break
		}
	}
	var exit *exec.ExitError
	if errors.As(processErr, &exit) {
		category += "; exit=" + strconv.Itoa(exit.ExitCode())
	}
	return errors.New(message + " (" + category + ")")
}
