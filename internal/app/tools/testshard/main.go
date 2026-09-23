package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/flidai/leapview/internal/platform/testing/testshard"
)

func main() {
	packageName := flag.String("package", "", "Go package whose top-level tests should be listed")
	listFile := flag.String("list-file", "", "file containing the compiled binary test list (instead of --package)")
	shardIndex := flag.Int("shard-index", -1, "zero-based shard index")
	shardCount := flag.Int("shard-count", 0, "total number of shards")
	flag.Parse()

	if (*packageName == "") == (*listFile == "") {
		fail(fmt.Errorf("exactly one of package or list-file is required"))
	}
	var output []byte
	var err error
	if *listFile != "" {
		output, err = os.ReadFile(*listFile)
	} else {
		output, err = exec.Command("go", "test", "-list", "^Test", *packageName).CombinedOutput()
	}
	if err != nil {
		fail(fmt.Errorf("list tests: %w\n%s", err, output))
	}
	selected, err := testshard.Select(testshard.ParseList(string(output)), *shardIndex, *shardCount)
	if err != nil {
		fail(err)
	}
	pattern, err := testshard.Pattern(selected)
	if err != nil {
		fail(err)
	}
	fmt.Println(pattern)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
