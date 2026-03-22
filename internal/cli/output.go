package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)

// OutputFormat controls how results are displayed.
type OutputFormat string

const (
	FormatJSON  OutputFormat = "json"
	FormatTable OutputFormat = "table"
)

// PrintJSON writes data as pretty-printed JSON to stdout.
func PrintJSON(data any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

// PrintTable writes data as an aligned table to stdout.
func PrintTable(headers []string, rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, strings.Join(headers, "\t"))
	_, _ = fmt.Fprintln(w, strings.Repeat("-\t", len(headers)))
	for _, row := range rows {
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	_ = w.Flush()
}

// PrintResult prints data in the specified format.
// For table format, it falls back to JSON if the data cannot be tabulated.
func PrintResult(format OutputFormat, data any) error {
	if format == FormatTable {
		// Try to convert to table-friendly format
		if m, ok := data.(map[string]any); ok {
			if items, ok := m["items"].([]any); ok {
				return printItemsAsTable(items)
			}
		}
	}
	return PrintJSON(data)
}

func printItemsAsTable(items []any) error {
	if len(items) == 0 {
		fmt.Println("(no results)")
		return nil
	}

	// Extract headers from first item
	first, ok := items[0].(map[string]any)
	if !ok {
		return PrintJSON(items)
	}

	var headers []string
	for k := range first {
		headers = append(headers, k)
	}

	var rows [][]string
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		var row []string
		for _, h := range headers {
			row = append(row, fmt.Sprintf("%v", m[h]))
		}
		rows = append(rows, row)
	}

	PrintTable(headers, rows)
	return nil
}
