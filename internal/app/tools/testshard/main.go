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
	shardIndex := flag.Int("shard-index", -1, "zero-based shard index")
	shardCount := flag.Int("shard-count", 0, "total number of shards")
	flag.Parse()

	if *packageName == "" {
		fail(fmt.Errorf("package is required"))
	}
	command := exec.Command("go", "test", "-list", "^Test", *packageName)
	output, err := command.CombinedOutput()
	if err != nil {
		fail(fmt.Errorf("list tests in %s: %w\n%s", *packageName, err, output))
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
