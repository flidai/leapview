package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const currentVersion = 1

type categoryBudget struct {
	LineLimit         int `json:"lineLimit"`
	MaxFilesOverLimit int `json:"maxFilesOverLimit"`
	MaxExcessLines    int `json:"maxExcessLines"`
	MaxLargestFile    int `json:"maxLargestFile"`
}

type suppressionBudget struct {
	Marker         string `json:"marker"`
	MaxOccurrences int    `json:"maxOccurrences"`
}

type budget struct {
	Version      int                          `json:"version"`
	Categories   map[string]categoryBudget    `json:"categories"`
	Suppressions map[string]suppressionBudget `json:"suppressions"`
}

type hotspot struct {
	Path  string `json:"path"`
	Lines int    `json:"lines"`
}

type categoryResult struct {
	Files          int       `json:"files"`
	FilesOverLimit int       `json:"filesOverLimit"`
	ExcessLines    int       `json:"excessLines"`
	LargestFile    int       `json:"largestFile"`
	Hotspots       []hotspot `json:"hotspots,omitempty"`
}

type report struct {
	Categories   map[string]categoryResult `json:"categories"`
	Suppressions map[string]int            `json:"suppressions"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("qualitybudget", flag.ContinueOnError)
	root := flags.String("root", ".", "repository root")
	budgetPath := flags.String("budget", ".quality/engineering-budget.json", "budget policy path")
	write := flags.Bool("write", false, "ratchet the committed baseline to current measurements")
	allowIncrease := flags.Bool("allow-increase", false, "permit an explicit baseline increase while writing")
	jsonOutput := flags.Bool("json", false, "render the report as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}

	absoluteRoot, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	policyPath := *budgetPath
	if !filepath.IsAbs(policyPath) {
		policyPath = filepath.Join(absoluteRoot, policyPath)
	}
	policy, err := readBudget(policyPath)
	if err != nil {
		return err
	}
	files, err := trackedFiles(absoluteRoot)
	if err != nil {
		return err
	}
	result, err := analyze(absoluteRoot, files, policy)
	if err != nil {
		return err
	}

	if *write {
		updated, problems := ratchet(policy, result, *allowIncrease)
		if len(problems) > 0 {
			return errors.New("quality budget increase requires --allow-increase:\n- " + strings.Join(problems, "\n- "))
		}
		if err := writeBudget(policyPath, updated); err != nil {
			return err
		}
		fmt.Printf("updated engineering quality budget: %s\n", filepath.ToSlash(*budgetPath))
		return nil
	}

	problems := evaluate(policy, result)
	if *jsonOutput {
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
	} else {
		printReport(policy, result)
	}
	if len(problems) > 0 {
		return errors.New("engineering quality budget exceeded:\n- " + strings.Join(problems, "\n- ") + "\nRun `task quality:budget:update` only after reducing debt, or explicitly review any policy increase.")
	}
	return nil
}

func readBudget(path string) (budget, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return budget{}, fmt.Errorf("read quality budget: %w", err)
	}
	var policy budget
	if err := json.Unmarshal(body, &policy); err != nil {
		return budget{}, fmt.Errorf("decode quality budget: %w", err)
	}
	if policy.Version != currentVersion {
		return budget{}, fmt.Errorf("quality budget version = %d, want %d", policy.Version, currentVersion)
	}
	for name, category := range policy.Categories {
		if category.LineLimit < 1 {
			return budget{}, fmt.Errorf("category %s has invalid line limit %d", name, category.LineLimit)
		}
	}
	for name, suppression := range policy.Suppressions {
		if suppression.Marker == "" || suppression.MaxOccurrences < 0 {
			return budget{}, fmt.Errorf("suppression %s is invalid", name)
		}
	}
	return policy, nil
}

func writeBudget(path string, policy budget) error {
	body, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return fmt.Errorf("encode quality budget: %w", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write quality budget: %w", err)
	}
	return nil
}

func trackedFiles(root string) ([]string, error) {
	command := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}
	parts := bytes.Split(output, []byte{0})
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			files = append(files, filepath.ToSlash(string(part)))
		}
	}
	return files, nil
}

func analyze(root string, files []string, policy budget) (report, error) {
	result := report{
		Categories:   make(map[string]categoryResult, len(policy.Categories)),
		Suppressions: make(map[string]int, len(policy.Suppressions)),
	}
	for _, path := range files {
		category := sourceCategory(path)
		if category == "" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			if os.IsNotExist(err) {
				// The index still lists tracked files removed by the working tree.
				// A deletion reduces debt and must not make the check unreadable.
				continue
			}
			return report{}, fmt.Errorf("read %s: %w", path, err)
		}
		if generatedSource(path, body) {
			continue
		}
		for name, suppression := range policy.Suppressions {
			result.Suppressions[name] += commentMarkerCount(body, suppression.Marker)
		}
		categoryPolicy, ok := policy.Categories[category]
		if !ok {
			continue
		}
		lines := lineCount(body)
		measurement := result.Categories[category]
		measurement.Files++
		if lines > measurement.LargestFile {
			measurement.LargestFile = lines
		}
		if lines > categoryPolicy.LineLimit {
			measurement.FilesOverLimit++
			measurement.ExcessLines += lines - categoryPolicy.LineLimit
			measurement.Hotspots = append(measurement.Hotspots, hotspot{Path: path, Lines: lines})
		}
		result.Categories[category] = measurement
	}
	for name := range policy.Categories {
		measurement := result.Categories[name]
		sort.Slice(measurement.Hotspots, func(i, j int) bool {
			if measurement.Hotspots[i].Lines == measurement.Hotspots[j].Lines {
				return measurement.Hotspots[i].Path < measurement.Hotspots[j].Path
			}
			return measurement.Hotspots[i].Lines > measurement.Hotspots[j].Lines
		})
		if len(measurement.Hotspots) > 5 {
			measurement.Hotspots = measurement.Hotspots[:5]
		}
		result.Categories[name] = measurement
	}
	return result, nil
}

func sourceCategory(path string) string {
	lower := strings.ToLower(filepath.ToSlash(path))
	if excludedSourcePath(lower) {
		return ""
	}
	base := filepath.Base(lower)
	switch filepath.Ext(base) {
	case ".go":
		if strings.HasSuffix(base, "_test.go") {
			return "go-test"
		}
		return "go-production"
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.Contains(lower, "/__tests__/") {
			return "typescript-test"
		}
		return "typescript-production"
	default:
		return ""
	}
}

func excludedSourcePath(path string) bool {
	if strings.HasSuffix(path, ".d.ts") {
		return true
	}
	wrapped := "/" + strings.Trim(path, "/") + "/"
	for _, segment := range []string{"/.git/", "/.tmp/", "/node_modules/", "/vendor/", "/dist/", "/gen/", "/generated/"} {
		if strings.Contains(wrapped, segment) {
			return true
		}
	}
	return false
}

func generatedSource(path string, body []byte) bool {
	if strings.HasSuffix(path, ".gen.go") || strings.HasSuffix(path, "_generated.go") {
		return true
	}
	for index, line := range bytes.Split(body, []byte{'\n'}) {
		if index >= 10 {
			break
		}
		trimmed := strings.TrimSpace(string(line))
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "// code generated") && strings.Contains(lower, "do not edit") {
			return true
		}
		if (strings.HasPrefix(lower, "//") || strings.HasPrefix(lower, "/*") || strings.HasPrefix(lower, "*")) && strings.Contains(lower, "@generated") {
			return true
		}
	}
	return false
}

func commentMarkerCount(body []byte, marker string) int {
	count := 0
	for index := 0; index < len(body); {
		switch body[index] {
		case '\'', '"', '`':
			index = quotedValueEnd(body, index)
		case '/':
			if index+1 >= len(body) {
				index++
				continue
			}
			switch body[index+1] {
			case '/':
				end := bytes.IndexByte(body[index:], '\n')
				if end < 0 {
					end = len(body)
				} else {
					end += index
				}
				count += bytes.Count(body[index:end], []byte(marker))
				index = end
			case '*':
				end := bytes.Index(body[index+2:], []byte("*/"))
				if end < 0 {
					end = len(body)
				} else {
					end += index + 4
				}
				count += bytes.Count(body[index:end], []byte(marker))
				index = end
			default:
				index++
			}
		default:
			index++
		}
	}
	return count
}

func quotedValueEnd(body []byte, start int) int {
	quote := body[start]
	for index := start + 1; index < len(body); index++ {
		if body[index] == '\\' && quote != '`' {
			index++
			continue
		}
		if body[index] == quote {
			return index + 1
		}
	}
	return len(body)
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

func evaluate(policy budget, result report) []string {
	var problems []string
	for _, name := range sortedCategoryNames(policy.Categories) {
		limit := policy.Categories[name]
		observed := result.Categories[name]
		if observed.FilesOverLimit > limit.MaxFilesOverLimit {
			problems = append(problems, fmt.Sprintf("%s files over %d lines = %d, budget %d", name, limit.LineLimit, observed.FilesOverLimit, limit.MaxFilesOverLimit))
		}
		if observed.ExcessLines > limit.MaxExcessLines {
			problems = append(problems, fmt.Sprintf("%s excess lines = %d, budget %d", name, observed.ExcessLines, limit.MaxExcessLines))
		}
		if observed.LargestFile > limit.MaxLargestFile {
			problems = append(problems, fmt.Sprintf("%s largest file = %d lines, budget %d", name, observed.LargestFile, limit.MaxLargestFile))
		}
	}
	for _, name := range sortedSuppressionNames(policy.Suppressions) {
		limit := policy.Suppressions[name]
		if result.Suppressions[name] > limit.MaxOccurrences {
			problems = append(problems, fmt.Sprintf("%s occurrences = %d, budget %d", name, result.Suppressions[name], limit.MaxOccurrences))
		}
	}
	return problems
}

func ratchet(policy budget, result report, allowIncrease bool) (budget, []string) {
	updated := cloneBudget(policy)
	var increases []string
	for name, category := range updated.Categories {
		observed := result.Categories[name]
		if !allowIncrease {
			if observed.FilesOverLimit > category.MaxFilesOverLimit || observed.ExcessLines > category.MaxExcessLines || observed.LargestFile > category.MaxLargestFile {
				increases = append(increases, name)
				continue
			}
		}
		category.MaxFilesOverLimit = observed.FilesOverLimit
		category.MaxExcessLines = observed.ExcessLines
		category.MaxLargestFile = observed.LargestFile
		updated.Categories[name] = category
	}
	for name, suppression := range updated.Suppressions {
		observed := result.Suppressions[name]
		if !allowIncrease && observed > suppression.MaxOccurrences {
			increases = append(increases, name)
			continue
		}
		suppression.MaxOccurrences = observed
		updated.Suppressions[name] = suppression
	}
	sort.Strings(increases)
	return updated, increases
}

func cloneBudget(policy budget) budget {
	cloned := budget{
		Version:      policy.Version,
		Categories:   make(map[string]categoryBudget, len(policy.Categories)),
		Suppressions: make(map[string]suppressionBudget, len(policy.Suppressions)),
	}
	for name, category := range policy.Categories {
		cloned.Categories[name] = category
	}
	for name, suppression := range policy.Suppressions {
		cloned.Suppressions[name] = suppression
	}
	return cloned
}

func printReport(policy budget, result report) {
	fmt.Println("Engineering quality budget")
	for _, name := range sortedCategoryNames(policy.Categories) {
		limit := policy.Categories[name]
		observed := result.Categories[name]
		fmt.Printf("- %s: %d files; %d over %d lines; %d excess; largest %d\n", name, observed.Files, observed.FilesOverLimit, limit.LineLimit, observed.ExcessLines, observed.LargestFile)
		for _, item := range observed.Hotspots {
			fmt.Printf("  - %s: %d lines\n", item.Path, item.Lines)
		}
	}
	for _, name := range sortedSuppressionNames(policy.Suppressions) {
		fmt.Printf("- %s: %d/%d\n", name, result.Suppressions[name], policy.Suppressions[name].MaxOccurrences)
	}
}

func sortedCategoryNames(values map[string]categoryBudget) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedSuppressionNames(values map[string]suppressionBudget) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
