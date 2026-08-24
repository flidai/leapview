// Command securityresults enforces the aggregate result contract for the
// required security workflow. It deliberately accepts only the four closed
// lane results so skipped, cancelled, and unknown states fail closed.
package main

import (
	"fmt"
	"io"
	"os"
)

const expectedLaneCount = 4

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(results []string, stderr io.Writer) int {
	if len(results) != expectedLaneCount {
		fmt.Fprintf(stderr, "expected exactly four security lane results, received %d\n", len(results))
		return 64
	}
	for _, result := range results {
		if result != "success" {
			fmt.Fprintf(stderr, "Security validation result: %s\n", result)
			return 1
		}
	}
	return 0
}
