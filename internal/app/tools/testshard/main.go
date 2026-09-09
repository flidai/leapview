package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/flidai/leapview/internal/platform/testing/testshard"
)

func main() {
	packageName := flag.String("package", "", "Go package whose top-level tests should be listed")
	shardIndex := flag.Int("shard-index", -1, "zero-based shard index")
	shardCount := flag.Int("shard-count", 0, "total number of shards")
	tags := flag.String("tags", "", "optional space-separated Go build tags used while listing tests")
	flag.Parse()

	if *packageName == "" {
		fail(fmt.Errorf("package is required"))
	}
	command := exec.Command("go", listTestArgs(*packageName, *tags)...)
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

func listTestArgs(packageName, tags string) []string {
	testArgs := []string{"test"}
	if strings.TrimSpace(tags) != "" {
		testArgs = append(testArgs, "-tags", tags)
	}
	return append(testArgs, "-list", "^Test", packageName)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
