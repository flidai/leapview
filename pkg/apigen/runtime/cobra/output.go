package cobra

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// OutputFormat represents the output format shared by generated Cobra commands.
type OutputFormat string

const (
	// OutputText renders human-friendly output such as tables and details.
	OutputText OutputFormat = "text"
	// OutputJSON renders machine-readable JSON output.
	OutputJSON OutputFormat = "json"
)

// GetTerminalWidth returns the terminal width or a default.
func GetTerminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 120
}

// IsTTY returns true if stdout is a terminal.
func IsTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

const maxColWidth = 50

// PrintTable renders tabular data to stdout using a simple columnar layout.
func PrintTable(w io.Writer, columns []string, rows [][]string) {
	if len(columns) == 0 {
		return
	}

	widths := make([]int, len(columns))
	for i, col := range columns {
		widths[i] = len(col)
	}
	for _, row := range rows {
		for i := 0; i < len(row) && i < len(widths); i++ {
			if len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
	}

	for i := range widths {
		if widths[i] > maxColWidth {
			widths[i] = maxColWidth
		}
	}

	if IsTTY() {
		maxWidth := GetTerminalWidth()
		colWidth := maxWidth / maxInt(len(columns), 1)
		if colWidth < 10 {
			colWidth = 10
		}
		for i := range widths {
			if widths[i] > colWidth {
				widths[i] = colWidth
			}
		}
	}

	for i := range rows {
		for j := range rows[i] {
			if j < len(widths) && len(rows[i][j]) > widths[j] {
				val := rows[i][j]
				if widths[j] > 3 && len(val) >= widths[j]-3 {
					rows[i][j] = truncateString(val, widths[j]-3) + "..."
				} else {
					rows[i][j] = truncateString(val, widths[j])
				}
			}
		}
	}

	for i, col := range columns {
		if i > 0 {
			_, _ = fmt.Fprint(w, "  ")
		}
		_, _ = fmt.Fprintf(w, "%-*s", widths[i], strings.ToUpper(col))
	}
	_, _ = fmt.Fprintln(w)

	for _, row := range rows {
		for i := 0; i < len(columns); i++ {
			if i > 0 {
				_, _ = fmt.Fprint(w, "  ")
			}
			val := ""
			if i < len(row) {
				val = row[i]
			}
			_, _ = fmt.Fprintf(w, "%-*s", widths[i], val)
		}
		_, _ = fmt.Fprintln(w)
	}
}

// PrintJSON outputs data as formatted JSON.
func PrintJSON(w io.Writer, data interface{}) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

// PrintDetail prints a single resource as key-value pairs.
func PrintDetail(w io.Writer, fields map[string]interface{}) {
	maxKeyLen := 0
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
		if len(k) > maxKeyLen {
			maxKeyLen = len(k)
		}
	}
	sortStrings(keys)
	for _, k := range keys {
		v := fields[k]
		padding := strings.Repeat(" ", maxKeyLen-len(k))
		_, _ = fmt.Fprintf(w, "%s:%s  %s\n", k, padding, FormatValue(v))
	}
}

// IsStdinTTY returns true if stdin is a terminal.
func IsStdinTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// ConfirmPrompt asks the user for confirmation.
func ConfirmPrompt(message string) bool {
	if !IsStdinTTY() {
		_, _ = fmt.Fprintln(os.Stderr, "Error: confirmation required but stdin is not a terminal. Use --yes to skip.")
		return false
	}
	_, _ = fmt.Fprintf(os.Stderr, "%s [y/N]: ", message)
	var response string
	_, _ = fmt.Scanln(&response)
	response = strings.ToLower(strings.TrimSpace(response))
	return response == "y" || response == "yes"
}

// ExtractField extracts a field from a generic map.
func ExtractField(data map[string]interface{}, field string) string {
	v, ok := data[field]
	if !ok || v == nil {
		return ""
	}
	return FormatValue(v)
}

// FormatValue formats a value for human-readable CLI display.
func FormatValue(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%g", val)
	case bool:
		return fmt.Sprintf("%t", val)
	case map[string]interface{}:
		b, _ := json.Marshal(val)
		return string(b)
	case []interface{}:
		b, _ := json.Marshal(val)
		return string(b)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ExtractRows extracts table rows from a paginated response.
func ExtractRows(data map[string]interface{}, columns []string) [][]string {
	items, ok := data["data"].([]interface{})
	if !ok {
		return nil
	}
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		row := make([]string, len(columns))
		for i, col := range columns {
			row[i] = ExtractField(m, col)
		}
		rows = append(rows, row)
	}
	return rows
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncateString(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	return string(runes[:width])
}
