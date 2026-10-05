// Command securitysast prepares a traced, read-only Go build and rejects
// incomplete CodeQL analysis. Vulnerability disposition is a separate policy.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("expected modules, integrity, or sarif subcommand")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	root := flags.String("root", ".", "repository root")
	input := flags.String("input", "", "raw analyzer SARIF file")
	category := flags.String("category", "", "expected CodeQL upload category")
	revision := flags.String("revision", "", "checkout commit captured before preparation")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "modules":
		modules, err := moduleDirectories(absRoot)
		if err != nil {
			return err
		}
		for _, dir := range modules {
			if _, err := fmt.Fprintf(os.Stdout, "%s\x00", dir); err != nil {
				return err
			}
		}
		return nil
	case "integrity":
		return checkIntegrity(ctx, absRoot, *revision)
	case "sarif":
		if *input == "" {
			return fmt.Errorf("raw SARIF input is required")
		}
		data, err := os.ReadFile(*input)
		if err != nil {
			return fmt.Errorf("read raw SARIF: %w", err)
		}
		return validateSARIF(data, *category)
	default:
		return fmt.Errorf("unknown SAST subcommand %q", args[0])
	}
}
