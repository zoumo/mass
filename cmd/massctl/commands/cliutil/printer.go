package cliutil

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	sigsyaml "sigs.k8s.io/yaml"
)

// OutputFormat controls how resources are rendered.
type OutputFormat string

const (
	FormatTable OutputFormat = "table"
	FormatWide  OutputFormat = "wide"
	FormatJSON  OutputFormat = "json"
	FormatYAML  OutputFormat = "yaml"
)

// ValidFormats returns all recognized output format strings.
func ValidFormats() []string {
	return []string{string(FormatTable), string(FormatWide), string(FormatJSON), string(FormatYAML)}
}

// Column defines a single column in table output.
type Column struct {
	// Header is the column title (e.g. "NAME", "STATE").
	Header string
	// Field extracts the column value from a resource item.
	Field func(any) string
	// Wide marks the column as visible only in wide output.
	Wide bool
}

// ResourcePrinter renders resources in the requested format.
type ResourcePrinter struct {
	Format  OutputFormat
	Columns []Column
}

// Print renders items to w. For json/yaml the raw objects are serialized.
// For table/wide the Column definitions are used to extract fields.
func (p *ResourcePrinter) Print(w io.Writer, items []any) error {
	switch p.Format {
	case FormatJSON:
		return p.printJSON(w, items)
	case FormatYAML:
		return p.printYAML(w, items)
	case FormatTable, FormatWide, "":
		return p.printTable(w, items)
	default:
		return fmt.Errorf("unknown output format %q (valid: %s)", p.Format, strings.Join(ValidFormats(), ", "))
	}
}

func (p *ResourcePrinter) printJSON(w io.Writer, items []any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if len(items) == 1 {
		return enc.Encode(items[0])
	}
	return enc.Encode(items)
}

func (p *ResourcePrinter) printYAML(w io.Writer, items []any) error {
	var obj any = items
	if len(items) == 1 {
		obj = items[0]
	}
	out, err := sigsyaml.Marshal(obj)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

func (p *ResourcePrinter) printTable(w io.Writer, items []any) error {
	wide := p.Format == FormatWide
	cols := p.activeColumns(wide)

	if len(cols) == 0 {
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)

	// Header row.
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = c.Header
	}
	fmt.Fprintln(tw, strings.Join(headers, "\t"))

	// Data rows.
	for _, item := range items {
		fields := make([]string, len(cols))
		for i, c := range cols {
			fields[i] = c.Field(item)
		}
		fmt.Fprintln(tw, strings.Join(fields, "\t"))
	}

	return tw.Flush()
}

// activeColumns returns columns visible for the current format.
func (p *ResourcePrinter) activeColumns(wide bool) []Column {
	var cols []Column
	for _, c := range p.Columns {
		if !c.Wide || wide {
			cols = append(cols, c)
		}
	}
	return cols
}
