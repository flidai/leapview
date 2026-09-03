package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const policyVersion = 1

const dateLayout = "2006-01-02"

type exception struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	Owner        string `json:"owner"`
	Reason       string `json:"reason"`
	ReviewedAt   string `json:"reviewedAt"`
	ReviewBy     string `json:"reviewBy"`
	MaximumLines int    `json:"maximumLines"`
}

type policy struct {
	Version    int         `json:"version"`
	Exceptions []exception `json:"exceptions"`
}

func main() {
	if err := run(os.Args[1:], time.Now().UTC()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, now time.Time) error {
	flags := flag.NewFlagSet("exceptionquality", flag.ContinueOnError)
	root := flags.String("root", ".", "repository root")
	policyPath := flags.String("policy", ".quality/engineering-exceptions.json", "reviewed exception policy")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	absoluteRoot, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	path := *policyPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(absoluteRoot, path)
	}
	exceptions, err := readPolicy(path)
	if err != nil {
		return err
	}
	return checkExceptions(absoluteRoot, exceptions, now)
}

func readPolicy(path string) (policy, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return policy{}, fmt.Errorf("read engineering exception policy: %w", err)
	}
	var exceptions policy
	if err := json.Unmarshal(body, &exceptions); err != nil {
		return policy{}, fmt.Errorf("decode engineering exception policy: %w", err)
	}
	if err := validatePolicy(exceptions); err != nil {
		return policy{}, err
	}
	return exceptions, nil
}

func validatePolicy(exceptions policy) error {
	if exceptions.Version != policyVersion {
		return fmt.Errorf("engineering exception policy version = %d, want %d", exceptions.Version, policyVersion)
	}
	seen := make(map[string]bool, len(exceptions.Exceptions))
	for _, item := range exceptions.Exceptions {
		if item.Path == "" || filepath.IsAbs(item.Path) || filepath.ToSlash(filepath.Clean(item.Path)) != item.Path || strings.HasPrefix(item.Path, "../") {
			return fmt.Errorf("engineering exception path %q is not a clean repository-relative path", item.Path)
		}
		if seen[item.Path] {
			return fmt.Errorf("engineering exception %s is declared more than once", item.Path)
		}
		seen[item.Path] = true
		if strings.TrimSpace(item.Kind) == "" || strings.TrimSpace(item.Owner) == "" {
			return fmt.Errorf("engineering exception %s requires kind and owner", item.Path)
		}
		if len(strings.Fields(item.Reason)) < 8 {
			return fmt.Errorf("engineering exception %s requires a specific justification", item.Path)
		}
		if item.MaximumLines < 1 {
			return fmt.Errorf("engineering exception %s has invalid maximumLines %d", item.Path, item.MaximumLines)
		}
		reviewedAt, err := time.Parse(dateLayout, item.ReviewedAt)
		if err != nil {
			return fmt.Errorf("engineering exception %s has invalid reviewedAt: %w", item.Path, err)
		}
		reviewBy, err := time.Parse(dateLayout, item.ReviewBy)
		if err != nil {
			return fmt.Errorf("engineering exception %s has invalid reviewBy: %w", item.Path, err)
		}
		if !reviewBy.After(reviewedAt) {
			return fmt.Errorf("engineering exception %s reviewBy must be after reviewedAt", item.Path)
		}
	}
	return nil
}

func checkExceptions(root string, exceptions policy, now time.Time) error {
	items := append([]exception(nil), exceptions.Exceptions...)
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	fmt.Println("Reviewed engineering exceptions")
	var problems []string
	for _, item := range items {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(item.Path)))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s cannot be read: %v", item.Path, err))
			continue
		}
		lines := lineCount(body)
		reviewBy, _ := time.Parse(dateLayout, item.ReviewBy)
		fmt.Printf("- %s: %d/%d lines; owner %s; review by %s\n", item.Path, lines, item.MaximumLines, item.Owner, item.ReviewBy)
		if lines > item.MaximumLines {
			problems = append(problems, fmt.Sprintf("%s has %d lines, exception maximum %d", item.Path, lines, item.MaximumLines))
		}
		if now.Truncate(24 * time.Hour).After(reviewBy) {
			problems = append(problems, fmt.Sprintf("%s review expired on %s", item.Path, item.ReviewBy))
		}
	}
	if len(problems) > 0 {
		return errors.New("engineering exception review failed:\n- " + strings.Join(problems, "\n- "))
	}
	return nil
}

func lineCount(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	lines := bytes.Count(body, []byte{'\n'})
	if body[len(body)-1] != '\n' {
		lines++
	}
	return lines
}
